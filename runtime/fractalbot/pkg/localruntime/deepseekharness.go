package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const defaultDeepSeekHarnessDeliveryTimeout = 20 * time.Second

// DeepSeekHarness sends prompts to one named DeepSeek Harness Desktop session
// through the app's explicitly enabled local Chromium DevTools endpoint. It
// deliberately has no durable queue: DeepSeek Harness does not expose one.
type DeepSeekHarness struct {
	CDPEndpoint    string
	TargetSelector string
	Timeout        time.Duration

	httpClient *http.Client
	dialer     *websocket.Dialer
}

func (DeepSeekHarness) Name() string { return "deepseekHarness" }

func (DeepSeekHarness) Capabilities() Capabilities {
	return Capabilities{WakesIdle: true, PresenceKnown: true}
}

func (d DeepSeekHarness) Deliver(ctx context.Context, target Target, req Request) Result {
	if req.Mode != Assign {
		return failed(req, errors.New("deepseekHarness supports assign only"))
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return failed(req, errors.New("prompt is empty"))
	}
	sessionID := strings.TrimSpace(target.Session)
	if sessionID == "" {
		return failed(req, errors.New("deepseekHarness target session is required"))
	}
	workspace := filepath.Base(filepath.Clean(strings.TrimSpace(target.Workspace)))
	if workspace == "." || workspace == string(filepath.Separator) || workspace == "" {
		return failed(req, errors.New("deepseekHarness target workspace is required"))
	}
	ctx, cancel := context.WithTimeout(ctx, d.deliveryTimeout())
	defer cancel()
	cdpTarget, err := d.target(ctx)
	if err != nil {
		return failed(req, err)
	}
	value, err := d.evaluate(ctx, cdpTarget.WebSocketDebuggerURL, deepSeekHarnessDeliveryScript(sessionID, workspace, req.Prompt))
	if err != nil {
		return failed(req, err)
	}
	if err := validateDeepSeekHarnessDelivery(value, sessionID, workspace); err != nil {
		return failed(req, err)
	}
	return Result{Status: Delivered, EnvelopeID: req.Envelope.ID, Location: fmt.Sprintf("DeepSeek Harness session %s in workspace %s", sessionID, workspace)}
}

// Presence reports whether an eligible DeepSeek Harness page is currently
// reachable through the configured local DevTools endpoint.
func (d DeepSeekHarness) Presence(ctx context.Context, _ Target) Presence {
	if strings.TrimSpace(d.CDPEndpoint) == "" {
		return Offline
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := d.target(ctx); err != nil {
		return Offline
	}
	return Online
}

func (d DeepSeekHarness) deliveryTimeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultDeepSeekHarnessDeliveryTimeout
}

type deepSeekHarnessCDPTarget struct {
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (d DeepSeekHarness) target(ctx context.Context) (deepSeekHarnessCDPTarget, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(d.CDPEndpoint), "/")
	if endpoint == "" {
		return deepSeekHarnessCDPTarget{}, errors.New("deepseekHarness cdpEndpoint is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/list", nil)
	if err != nil {
		return deepSeekHarnessCDPTarget{}, fmt.Errorf("build DeepSeek Harness CDP request: %w", err)
	}
	client := d.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return deepSeekHarnessCDPTarget{}, fmt.Errorf("query DeepSeek Harness CDP targets: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return deepSeekHarnessCDPTarget{}, fmt.Errorf("query DeepSeek Harness CDP targets: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var targets []deepSeekHarnessCDPTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return deepSeekHarnessCDPTarget{}, fmt.Errorf("decode DeepSeek Harness CDP targets: %w", err)
	}
	selector := strings.ToLower(strings.TrimSpace(d.TargetSelector))
	for _, target := range targets {
		if !isDeepSeekHarnessTarget(target) {
			continue
		}
		if selector == "" || strings.Contains(strings.ToLower(target.Title), selector) || strings.Contains(strings.ToLower(target.URL), selector) {
			return target, nil
		}
	}
	if selector != "" {
		return deepSeekHarnessCDPTarget{}, fmt.Errorf("no DeepSeek Harness CDP target matched %q", d.TargetSelector)
	}
	return deepSeekHarnessCDPTarget{}, errors.New("DeepSeek Harness CDP has no eligible app target")
}

func isDeepSeekHarnessTarget(target deepSeekHarnessCDPTarget) bool {
	if target.Type != "page" || strings.TrimSpace(target.WebSocketDebuggerURL) == "" {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(target.URL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "dsh-app") && strings.EqualFold(parsed.Host, "app")
}

func (d DeepSeekHarness) evaluate(ctx context.Context, endpoint, expression string) (any, error) {
	dialer := d.dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to DeepSeek Harness CDP: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.SetReadDeadline(deadline)
	}
	if err := conn.WriteJSON(map[string]any{
		"id":     1,
		"method": "Runtime.evaluate",
		"params": map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true, "userGesture": true},
	}); err != nil {
		return nil, fmt.Errorf("write DeepSeek Harness CDP evaluation: %w", err)
	}
	for {
		var response struct {
			ID    int `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Result struct {
				Result struct {
					Value json.RawMessage `json:"value"`
				} `json:"result"`
				ExceptionDetails *struct {
					Text string `json:"text"`
				} `json:"exceptionDetails"`
			} `json:"result"`
		}
		if err := conn.ReadJSON(&response); err != nil {
			return nil, fmt.Errorf("read DeepSeek Harness CDP evaluation: %w", err)
		}
		if response.ID != 1 {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("DeepSeek Harness CDP evaluation failed: %s", response.Error.Message)
		}
		if response.Result.ExceptionDetails != nil {
			return nil, fmt.Errorf("DeepSeek Harness composer evaluation failed: %s", response.Result.ExceptionDetails.Text)
		}
		if len(response.Result.Result.Value) == 0 {
			return nil, errors.New("DeepSeek Harness composer evaluation returned no value")
		}
		var value any
		if err := json.Unmarshal(response.Result.Result.Value, &value); err != nil {
			return nil, fmt.Errorf("decode DeepSeek Harness composer result: %w", err)
		}
		return value, nil
	}
}

func validateDeepSeekHarnessDelivery(value any, sessionID, workspace string) error {
	result, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("DeepSeek Harness delivery returned %T, expected object", value)
	}
	if accepted, ok := result["ok"].(bool); !ok || !accepted {
		return fmt.Errorf("DeepSeek Harness delivery was not accepted: %s", cdpDeliveryErrorDetail(result))
	}
	if submitted, ok := result["submitted"].(bool); !ok || !submitted {
		return fmt.Errorf("DeepSeek Harness delivery did not submit the prompt: %s", cdpDeliveryErrorDetail(result))
	}
	if selected, ok := result["session"].(string); !ok || selected != sessionID {
		return fmt.Errorf("DeepSeek Harness delivery targeted an unexpected session: %s", cdpDeliveryErrorDetail(result))
	}
	if selected, ok := result["workspace"].(string); !ok || selected != workspace {
		return fmt.Errorf("DeepSeek Harness delivery targeted an unexpected workspace: %s", cdpDeliveryErrorDetail(result))
	}
	return nil
}

func cdpDeliveryErrorDetail(result map[string]any) string {
	if detail, ok := result["error"].(string); ok && strings.TrimSpace(detail) != "" {
		return strings.TrimSpace(detail)
	}
	return "unknown error"
}

func deepSeekHarnessDeliveryScript(sessionID, workspace, prompt string) string {
	encodedSession, _ := json.Marshal(sessionID)
	encodedWorkspace, _ := json.Marshal(workspace)
	encodedPrompt, _ := json.Marshal(prompt)
	return fmt.Sprintf(`(async () => {
  const targetSession = %s;
  const targetWorkspace = %s;
  const text = %s;
  const visible = (element) => {
    if (!element) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  };
  const editable = (element) => element && (element.isContentEditable || element.getAttribute("contenteditable") === "true" || element.tagName === "TEXTAREA" || element.getAttribute("role") === "textbox");

  // DSH renders its workspace tree in flat order. A session must immediately
  // follow its workspace until the next workspace row; checking that parent
  // prevents a same-titled session in another workspace from receiving input.
  const rows = () => Array.from(document.querySelectorAll("[data-row-key]")).filter(visible);
  const key = "session:" + targetSession;
  const sessionRow = () => rows().find((row) => row.getAttribute("data-row-key") === key);
  const targetWorkspaceRow = () => rows().find((row) => (row.getAttribute("data-row-key") || "").startsWith("workspace:") && row.innerText.trim() === targetWorkspace);
  const workspaceFor = (row) => {
    const items = rows();
    const index = items.indexOf(row);
    for (let cursor = index - 1; cursor >= 0; cursor -= 1) {
      if ((items[cursor].getAttribute("data-row-key") || "").startsWith("workspace:")) return items[cursor];
    }
    return null;
  };
  let selected = sessionRow();
  if (!selected) {
    const workspaceRow = targetWorkspaceRow();
    if (!workspaceRow) return { ok: false, submitted: false, error: "configured DeepSeek Harness workspace is not visible" };
    if (workspaceRow.getAttribute("aria-expanded") !== "true") workspaceRow.click();
    const deadline = Date.now() + 2000;
    while (Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 25));
      selected = sessionRow();
      if (selected) break;
    }
  }
  if (!selected) return { ok: false, submitted: false, error: "target DeepSeek Harness session is not visible" };
  const workspaceRow = workspaceFor(selected);
  if (!workspaceRow || workspaceRow.innerText.trim() !== targetWorkspace) {
    return { ok: false, submitted: false, error: "target session is not in the configured DeepSeek Harness workspace" };
  }
  if (selected.getAttribute("aria-selected") !== "true") selected.click();
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
    selected = sessionRow();
    if (selected && selected.getAttribute("aria-selected") === "true") break;
  }
  if (!selected || selected.getAttribute("aria-selected") !== "true") {
    return { ok: false, submitted: false, error: "DeepSeek Harness did not activate the target session" };
  }

  const composer = () => Array.from(document.querySelectorAll("textarea,[data-composer-input],[contenteditable=true],[role=textbox],input[type=text]")).find((element) => visible(element) && editable(element));
  const composerText = (element) => element && (element.isContentEditable || element.getAttribute("contenteditable") === "true") ? element.innerText.trim() : String(element?.value || "").trim();
  const sendButton = () => Array.from(document.querySelectorAll("button")).filter(visible).find((candidate) => {
    const label = [candidate.getAttribute("aria-label"), candidate.getAttribute("title"), candidate.getAttribute("data-testid"), candidate.innerText].filter(Boolean).join(" ").trim();
    return /^(send( message)?|submit|发送|提交)$/i.test(label) || candidate.type === "submit";
  });
  const waitUntil = async (predicate, deadline) => {
    while (Date.now() < deadline) {
      const value = predicate();
      if (value) return value;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    return null;
  };

  const readyDeadline = Date.now() + 8000;
  let input = composer();
  let button = await waitUntil(() => {
    input = composer();
    return input && sendButton();
  }, readyDeadline);
  if (!input) return { ok: false, submitted: false, error: "no visible DeepSeek Harness composer found" };
  if (!button) return { ok: false, submitted: false, error: "target DeepSeek Harness session is still generating" };
  if (composerText(input) !== "") return { ok: false, submitted: false, error: "target DeepSeek Harness composer already has a draft" };

  input.focus();
  if (input.isContentEditable || input.getAttribute("contenteditable") === "true") {
    const range = document.createRange();
    range.selectNodeContents(input);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    document.execCommand("insertText", false, text);
  } else {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input), "value")?.set;
    setter ? setter.call(input, text) : input.value = text;
  }
  input.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", data: text }));
  input.dispatchEvent(new Event("change", { bubbles: true }));

  button = await waitUntil(() => {
    const candidate = sendButton();
    if (!candidate || candidate.disabled || candidate.getAttribute("aria-disabled") === "true") return null;
    return candidate;
  }, Date.now() + 3000);
  if (!button) return { ok: false, submitted: false, error: "DeepSeek Harness did not enable its send button after composing the prompt" };
  button.click();
  const accepted = await waitUntil(() => {
    const current = composer();
    const stop = Array.from(document.querySelectorAll("button")).filter(visible).some((candidate) => /^(stop|stop generating|停止|停止生成)$/i.test((candidate.getAttribute("aria-label") || candidate.innerText || "").trim()));
    return stop || composerText(current) === "";
  }, Date.now() + 3000);
  if (!accepted) return { ok: false, submitted: false, error: "DeepSeek Harness did not confirm prompt submission" };
  return { ok: true, submitted: true, submitMethod: "button", session: targetSession, workspace: targetWorkspace };
})()`, string(encodedSession), string(encodedWorkspace), string(encodedPrompt))
}
