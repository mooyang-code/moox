package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signTimestamp 这一毫秒算出的签名同时含有 + 和 /，用来检查签名在地址里做了 URL 编码。
const signTimestamp int64 = 1700000000129

func TestDingTalkSenderSignsAndSendsMarkdown(t *testing.T) {
	var gotQuery url.Values
	var gotRawQuery string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotRawQuery = r.URL.Query(), r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	const secret = "SECtest+secret/with=symbols"
	sender, err := newDingTalkSender(server.URL+"/robot/send?access_token=abc", secret, server.Client())
	if err != nil {
		t.Fatalf("newDingTalkSender() error = %v", err)
	}
	sender.now = func() time.Time { return time.UnixMilli(signTimestamp) }
	message := Message{Key: "deployment:storage:storage-primary", Severity: SeverityCritical, Title: "storage-primary 不可用", Body: "第一行\n第二行", Labels: map[string]string{"host": "storage"}}
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	timestamp := strconv.FormatInt(signTimestamp, 10)
	if gotQuery.Get("access_token") != "abc" || gotQuery.Get("timestamp") != timestamp {
		t.Fatalf("query = %v", gotQuery)
	}
	// 按钉钉文档独立算一遍：HMAC-SHA256(key=密钥, 内容=时间戳+"\n"+密钥)，再 base64；服务端收到的是 URL 解码后的值。
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if gotQuery.Get("sign") != want {
		t.Fatalf("sign = %q, want %q", gotQuery.Get("sign"), want)
	}
	// 这个时间戳算出的签名里有 + 和 /：线路上必须是 URL 编码后的形式，否则钉钉会把 + 当成空格、验签失败。
	if !strings.ContainsAny(want, "+/") || !strings.Contains(gotRawQuery, "sign="+url.QueryEscape(want)) {
		t.Fatalf("raw query %q does not carry the URL-encoded sign of %q", gotRawQuery, want)
	}
	markdown, _ := gotBody["markdown"].(map[string]any)
	title, _ := markdown["title"].(string)
	text, _ := markdown["text"].(string)
	if gotBody["msgtype"] != "markdown" || !strings.HasPrefix(title, "MooX [严重] ") || !strings.HasPrefix(text, "**MooX [严重] storage-primary 不可用**") {
		t.Fatalf("payload = %v", gotBody)
	}
	for _, want := range []string{"第一行\n\n第二行", "> host: storage", "告警标识：deployment:storage:storage-primary"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text %q does not contain %q", text, want)
		}
	}
}

func TestDingTalkSenderWithoutSecretKeepsURL(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()
	sender, err := newDingTalkSender(server.URL+"/robot/send?access_token=abc", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), Message{Severity: SeverityInfo, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if rawQuery != "access_token=abc" {
		t.Fatalf("query = %q", rawQuery)
	}
}

func TestDingTalkSenderRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"keywords not in content"}`))
	}))
	defer server.Close()
	sender, err := newDingTalkSender(server.URL+"/robot/send?access_token=topsecret", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{Severity: SeverityWarning, Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "310000") || !strings.Contains(err.Error(), "keywords not in content") {
		t.Fatalf("Send() error = %v", err)
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("error leaks webhook URL: %v", err)
	}
}

func TestDingTalkChannelValidation(t *testing.T) {
	valid := ChannelConfig{Type: ChannelTypeDingTalk, WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=abc", Secret: "SECabc"}
	if _, err := NewSender(valid); err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
	for name, cfg := range map[string]ChannelConfig{
		"其他域名":           {Type: ChannelTypeDingTalk, WebhookURL: "https://evil.example/robot/send?access_token=abc"},
		"缺 access_token": {Type: ChannelTypeDingTalk, WebhookURL: "https://oapi.dingtalk.com/robot/send"},
		"非 HTTPS":        {Type: ChannelTypeDingTalk, WebhookURL: "http://oapi.dingtalk.com/robot/send?access_token=abc"},
		"企业微信不接受密钥":      {Type: ChannelTypeWeCom, WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x", Secret: "s"},
	} {
		if _, err := NewSender(cfg); err == nil {
			t.Fatalf("%s：应当被拒绝", name)
		}
	}
}
