package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeishuSenderSendsInteractiveMessage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, signed := payload["sign"]; signed || payload["timestamp"] != nil {
			t.Fatalf("没有配置密钥时不应带签名字段：%v", payload)
		}
		if payload["msg_type"] != "text" {
			t.Fatalf("msg_type = %v, want text", payload["msg_type"])
		}
		content := payload["content"].(map[string]any)
		text, _ := content["text"].(string)
		if !strings.Contains(text, "告警标识: test-key") || !strings.Contains(text, "agent_id=AB12") || !strings.Contains(text, "[严重]") {
			t.Fatalf("text lost notification metadata: %q", text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	sender, err := newFeishuSender("https://open.feishu.cn/hook", "", server.Client())
	if err != nil {
		t.Fatalf("NewFeishuSender() error = %v", err)
	}
	sender.client = server.Client()
	sender.webhookURL = server.URL
	if err := sender.Send(context.Background(), Message{Key: "test-key", Severity: SeverityCritical, Title: "测试", Body: "消息", Labels: map[string]string{"agent_id": "AB12"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

// 启用签名校验时，请求体带秒级时间戳和签名。签名按飞书文档独立算一遍：key 是“时间戳 + 换行 + 密钥”，内容为空。
func TestFeishuSenderSignsRequestWhenSecretIsSet(t *testing.T) {
	var payload map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	const secret = "feishu-sign-secret"
	sender, err := newFeishuSender(server.URL, secret, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	sender.now = func() time.Time { return time.Unix(1599360473, 987_000_000) }
	if err := sender.Send(context.Background(), Message{Severity: SeverityWarning, Title: "测试"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	mac := hmac.New(sha256.New, []byte("1599360473\n"+secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if payload["timestamp"] != "1599360473" || payload["sign"] != want {
		t.Fatalf("timestamp = %v, sign = %v, want sign %q", payload["timestamp"], payload["sign"], want)
	}
	if payload["msg_type"] != "text" {
		t.Fatalf("payload = %v", payload)
	}
}

// 飞书验签失败时 HTTP 仍是 200，以返回体的 code 为准。
func TestFeishuSenderReportsSignatureMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`))
	}))
	defer server.Close()
	sender, err := newFeishuSender(server.URL, "wrong-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{Severity: SeverityInfo, Title: "测试"})
	if err == nil || !strings.Contains(err.Error(), "19021") || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestFeishuChannelAcceptsSecret(t *testing.T) {
	if _, err := NewSender(ChannelConfig{Type: ChannelTypeFeishu, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/x", Secret: "s"}); err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
}
