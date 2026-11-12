package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/netguard"
)

func TestWebhookSenderPostsSignedJSON(t *testing.T) {
	var header http.Header
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender, err := newWebhookSender(server.URL+"/hook", "whsec_test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	sender.now = func() time.Time { return time.UnixMilli(1700000000123) }
	message := Message{Key: "k1", Severity: SeverityWarning, Title: "标题", Body: "正文", Labels: map[string]string{"host": "control"}}
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	var got struct {
		Key, Severity, Title, Body string
		Labels                     map[string]string
		SentAt                     string `json:"sent_at"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != "k1" || got.Severity != "warning" || got.Title != "标题" || got.Body != "正文" || got.Labels["host"] != "control" || got.SentAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("payload = %+v", got)
	}
	if header.Get("Content-Type") != "application/json" || header.Get("X-MooX-Timestamp") != "1700000000123" {
		t.Fatalf("header = %v", header)
	}
	if want := WebhookSignature("whsec_test", "1700000000123", raw); header.Get("X-MooX-Signature") != want {
		t.Fatalf("signature = %q, want %q", header.Get("X-MooX-Signature"), want)
	}
}

func TestWebhookSenderWithoutSecretSendsNoSignature(t *testing.T) {
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { header = r.Header.Clone() }))
	defer server.Close()
	sender, err := newWebhookSender(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), Message{Severity: SeverityInfo}); err != nil {
		t.Fatal(err)
	}
	if header.Get("X-MooX-Signature") != "" || header.Get("X-MooX-Timestamp") != "" {
		t.Fatalf("unexpected signature headers: %v", header)
	}
}

func TestWebhookSenderRejectsNon2xxAndRedirect(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusInternalServerError} {
		var followed atomic.Bool
		// 302 指向同一台服务器上会返回 200 的路径：跟随了重定向就会被当成送达。
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/elsewhere" {
				followed.Store(true)
				return
			}
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(status)
		}))
		sender, err := newWebhookSender(server.URL+"/hook?token=topsecret", "", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		err = sender.Send(context.Background(), Message{Severity: SeverityInfo})
		server.Close()
		if err == nil || strings.Contains(err.Error(), "topsecret") {
			t.Fatalf("status %d: Send() error = %v", status, err)
		}
		if followed.Load() {
			t.Fatalf("status %d: 不应跟随重定向", status)
		}
	}
}

// 发送器的拨号必须经过地址防护：回环、内网、链路本地地址，以及解析到这些地址的域名，都在拨号前被拒绝。
// 直接取出 Transport 的 DialContext 来断言具体的错误，不依赖“连不上所以报错”。
func TestWebhookSenderDialsOnlyPublicAddresses(t *testing.T) {
	sender, err := NewWebhookSender("https://alerts.example.com/hook", "")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := sender.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("webhook transport = %#v，应是不走代理的 http.Transport", sender.client.Transport)
	}
	for _, address := range []string{"127.0.0.1:443", "10.0.0.8:443", "169.254.169.254:443", "[::1]:443", "localhost:443"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := transport.DialContext(ctx, "tcp", address)
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, netguard.ErrAddressBlocked) {
			t.Fatalf("%s：err = %v，应被地址防护拒绝", address, err)
		}
	}
}

// 直接填了非公网地址的，添加渠道时就拒绝。
func TestWebhookChannelRejectsNonPublicLiteralHost(t *testing.T) {
	for _, target := range []string{"https://127.0.0.1:9/hook", "https://10.0.0.8/hook", "https://169.254.169.254/latest", "https://[::1]/hook", "https://localhost/hook"} {
		if _, err := NewSender(ChannelConfig{Type: ChannelTypeWebhook, WebhookURL: target}); err == nil {
			t.Fatalf("%s：应当被拒绝", target)
		}
	}
}

func TestWebhookChannelValidation(t *testing.T) {
	for name, cfg := range map[string]ChannelConfig{
		"非 HTTPS": {Type: ChannelTypeWebhook, WebhookURL: "http://example.com/hook"},
		"内嵌账号":    {Type: ChannelTypeWebhook, WebhookURL: "https://user:pass@example.com/hook"},
		"地址太长":    {Type: ChannelTypeWebhook, WebhookURL: "https://example.com/" + strings.Repeat("a", 2100)},
		"密钥带换行":   {Type: ChannelTypeWebhook, WebhookURL: "https://example.com/hook", Secret: "a\nb"},
	} {
		if _, err := NewSender(cfg); err == nil {
			t.Fatalf("%s：应当被拒绝", name)
		}
	}
}
