package notification

import "testing"

func TestNewSenderUsesConfiguredChannel(t *testing.T) {
	if _, err := NewSender(ChannelConfig{Type: ChannelTypeFeishu, WebhookURL: "https://open.feishu.cn/hook"}); err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
}

func TestNewSenderRejectsUnknownChannel(t *testing.T) {
	if _, err := NewSender(ChannelConfig{Type: ChannelType("sms"), WebhookURL: "https://open.feishu.cn/hook"}); err == nil {
		t.Fatal("NewSender() should reject unknown channel")
	}
}

func TestNewSenderRejectsUntrustedWebhookHost(t *testing.T) {
	for _, cfg := range []ChannelConfig{
		{Type: ChannelTypeWeCom, WebhookURL: "https://evil.example/cgi-bin/webhook/send?key=x"},
		{Type: ChannelTypeFeishu, WebhookURL: "https://evil.example/hook"},
	} {
		if _, err := NewSender(cfg); err == nil {
			t.Fatalf("untrusted webhook host accepted: %+v", cfg)
		}
	}
}
