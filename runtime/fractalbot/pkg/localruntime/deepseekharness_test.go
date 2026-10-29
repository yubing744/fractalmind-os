package localruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDeepSeekHarnessDeliversToDSHAppTarget(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Expression string `json:"expression"`
			} `json:"params"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "Runtime.evaluate" || !strings.Contains(request.Params.Expression, `const text = "wake up"`) {
			t.Errorf("unexpected CDP request: %#v", request)
		}
		_ = conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{"result": map[string]any{"value": map[string]any{"ok": true, "submitted": true}}}})
	}))
	defer ws.Close()
	wsURL := "ws" + strings.TrimPrefix(ws.URL, "http")

	cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]deepSeekHarnessCDPTarget{{Type: "page", Title: "DeepSeek Harness", URL: "dsh-app://app/", WebSocketDebuggerURL: wsURL}})
	}))
	defer cdp.Close()

	runtime := DeepSeekHarness{CDPEndpoint: cdp.URL, Timeout: time.Second}
	result := runtime.Deliver(context.Background(), Target{}, request("wake up"))
	if result.Status != Delivered || result.Err != nil || result.Location != "DeepSeek Harness active composer" {
		t.Fatalf("deliver: %+v", result)
	}
	if runtime.Presence(context.Background(), Target{}) != Online {
		t.Fatal("expected online DeepSeek Harness target")
	}
}

func TestDeepSeekHarnessRejectsNonHarnessTarget(t *testing.T) {
	cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]deepSeekHarnessCDPTarget{{Type: "page", Title: "unrelated", URL: "https://example.invalid", WebSocketDebuggerURL: "ws://example.invalid"}})
	}))
	defer cdp.Close()

	runtime := DeepSeekHarness{CDPEndpoint: cdp.URL}
	result := runtime.Deliver(context.Background(), Target{}, request("do not leak this prompt"))
	if result.Status != Failed || result.Err == nil || !strings.Contains(result.Err.Error(), "no eligible app target") {
		t.Fatalf("deliver: %+v", result)
	}
	if strings.Contains(result.Err.Error(), "do not leak") {
		t.Fatalf("prompt leaked into error: %v", result.Err)
	}
	if runtime.Presence(context.Background(), Target{}) != Offline {
		t.Fatal("unrelated page must not be online")
	}
}
