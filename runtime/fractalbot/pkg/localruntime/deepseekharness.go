package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const defaultDeepSeekHarnessDeliveryTimeout = 20 * time.Second

// DeepSeekHarness sends prompts to the active DeepSeek Harness Desktop page
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

func (d DeepSeekHarness) Deliver(ctx context.Context, _ Target, req Request) Result {
	if req.Mode != Assign {
		return failed(req, errors.New("deepseekHarness supports assign only"))
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return failed(req, errors.New("prompt is empty"))
	}
	ctx, cancel := context.WithTimeout(ctx, d.deliveryTimeout())
	defer cancel()
	target, err := d.target(ctx)
	if err != nil {
		return failed(req, err)
	}
	value, err := d.evaluate(ctx, target.WebSocketDebuggerURL, deepSeekHarnessDeliveryScript(req.Prompt))
	if err != nil {
		return failed(req, err)
	}
	if err := validateDeepSeekHarnessDelivery(value); err != nil {
		return failed(req, err)
	}
	return Result{Status: Delivered, EnvelopeID: req.Envelope.ID, Location: "DeepSeek Harness active composer"}
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

func validateDeepSeekHarnessDelivery(value any) error {
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
	return nil
}

func cdpDeliveryErrorDetail(result map[string]any) string {
	if detail, ok := result["error"].(string); ok && strings.TrimSpace(detail) != "" {
		return strings.TrimSpace(detail)
	}
	return "unknown error"
}

func deepSeekHarnessDeliveryScript(prompt string) string {
	encoded, _ := json.Marshal(prompt)
	return fmt.Sprintf(`(async () => {
  const text = %s;
  const visible = (element) => {
    if (!element) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  };
  const editable = (element) => element && (element.isContentEditable || element.getAttribute("contenteditable") === "true" || element.tagName === "TEXTAREA" || element.getAttribute("role") === "textbox");
  const inputs = Array.from(document.querySelectorAll("textarea,[contenteditable=true],[role=textbox],input[type=text]")).filter((element) => visible(element) && editable(element));
  const input = inputs[0];
  if (!input) return { ok: false, submitted: false, error: "no visible DeepSeek Harness composer found" };
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
  const button = Array.from(document.querySelectorAll("button")).filter(visible).find((candidate) => {
    if (candidate.disabled || candidate.getAttribute("aria-disabled") === "true") return false;
    const label = [candidate.getAttribute("aria-label"), candidate.getAttribute("title"), candidate.getAttribute("data-testid"), candidate.innerText, candidate.type].filter(Boolean).join(" ");
    return /(^|\\s)(send|submit|run)(\\s|$)|发送|提交|运行/i.test(label) || candidate.type === "submit";
  });
  if (!button) return { ok: false, submitted: false, error: "no enabled DeepSeek Harness send button found" };
  button.click();
  return { ok: true, submitted: true, submitMethod: "button" };
})()`, string(encoded))
}
