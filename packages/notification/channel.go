package notification

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

type ChannelType string

const (
	ChannelTypeWeCom  ChannelType = "wecom"
	ChannelTypeFeishu ChannelType = "feishu"
)

type ChannelConfig struct {
	Type       ChannelType
	WebhookURL string
}

func (c ChannelConfig) Validate() error {
	switch c.Type {
	case ChannelTypeWeCom, ChannelTypeFeishu:
	default:
		return errors.New("notification: unsupported channel type")
	}
	if c.WebhookURL == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(c.WebhookURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("notification: webhook URL must be a valid HTTPS URL")
	}
	if !allowedWebhookHost(c.Type, parsed.Hostname()) {
		return errors.New("notification: webhook host is not approved for the selected platform")
	}
	return nil
}

func allowedWebhookHost(channelType ChannelType, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch channelType {
	case ChannelTypeWeCom:
		return host == "qyapi.weixin.qq.com"
	case ChannelTypeFeishu:
		return host == "open.feishu.cn" || strings.HasSuffix(host, ".feishu.cn") || strings.HasSuffix(host, ".larksuite.com")
	default:
		return false
	}
}

func NewSender(config ChannelConfig) (Sender, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.WebhookURL == "" {
		return noopSender{}, nil
	}
	switch config.Type {
	case ChannelTypeWeCom:
		return NewWeComSender(config.WebhookURL)
	case ChannelTypeFeishu:
		return NewFeishuSender(config.WebhookURL)
	default:
		return nil, errors.New("notification: unsupported channel type")
	}
}

type noopSender struct{}

func (noopSender) Send(_ context.Context, _ Message) error { return nil }
