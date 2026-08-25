package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeishuSenderSendsInteractiveMessage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["msg_type"] != "text" {
			t.Fatalf("msg_type = %v, want text", payload["msg_type"])
		}
		content := payload["content"].(map[string]any)
		text, _ := content["text"].(string)
		if !strings.Contains(text, "告警标识: test-key") || !strings.Contains(text, "agent_id=AB12") || !strings.Contains(text, "[critical]") {
			t.Fatalf("text lost notification metadata: %q", text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	sender, err := newFeishuSender("https://open.feishu.cn/hook", server.Client())
	if err != nil {
		t.Fatalf("NewFeishuSender() error = %v", err)
	}
	sender.client = server.Client()
	sender.webhookURL = server.URL
	if err := sender.Send(context.Background(), Message{Key: "test-key", Severity: SeverityCritical, Title: "测试", Body: "消息", Labels: map[string]string{"agent_id": "AB12"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}
