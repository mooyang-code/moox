package notification

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/mooyang-code/moox/packages/netguard"
)

type ChannelType string

const (
	ChannelTypeWeCom    ChannelType = "wecom"
	ChannelTypeFeishu   ChannelType = "feishu"
	ChannelTypeDingTalk ChannelType = "dingtalk"
	// ChannelTypeWebhook 是通用 Webhook：向任意公网 HTTPS 地址 POST 固定格式的 JSON。
	ChannelTypeWebhook ChannelType = "webhook"
)

const (
	maxWebhookURLBytes = 2048
	maxSecretBytes     = 256
)

// ChannelTypes 按展示顺序列出全部渠道类型。
func ChannelTypes() []ChannelType {
	return []ChannelType{ChannelTypeWeCom, ChannelTypeFeishu, ChannelTypeDingTalk, ChannelTypeWebhook}
}

// ValidChannelType 报告 value 是否是受支持的渠道类型。
func ValidChannelType(value string) bool {
	switch ChannelType(value) {
	case ChannelTypeWeCom, ChannelTypeFeishu, ChannelTypeDingTalk, ChannelTypeWebhook:
		return true
	default:
		return false
	}
}

// ChannelLabel 返回渠道类型的中文名；未知类型原样返回。
func ChannelLabel(value string) string {
	switch ChannelType(value) {
	case ChannelTypeWeCom:
		return "企业微信"
	case ChannelTypeFeishu:
		return "飞书"
	case ChannelTypeDingTalk:
		return "钉钉"
	case ChannelTypeWebhook:
		return "通用 Webhook"
	default:
		return value
	}
}

// SupportsSecret 报告该类型的渠道是否可以配置密钥（钉钉的加签密钥、通用 Webhook 的签名密钥）。
func SupportsSecret(value string) bool {
	return ChannelType(value) == ChannelTypeDingTalk || ChannelType(value) == ChannelTypeWebhook
}

type ChannelConfig struct {
	Type       ChannelType
	WebhookURL string
	// Secret 选填：钉钉机器人的加签密钥，或通用 Webhook 的签名密钥。企业微信、飞书不使用。
	Secret string
}

func (c ChannelConfig) Validate() error {
	if !ValidChannelType(string(c.Type)) {
		return errors.New("notification: unsupported channel type")
	}
	if c.Secret != "" {
		if !SupportsSecret(string(c.Type)) {
			return errors.New("notification: this channel type does not take a secret")
		}
		if len(c.Secret) > maxSecretBytes || strings.ContainsAny(c.Secret, "\r\n") {
			return errors.New("notification: invalid channel secret")
		}
	}
	if c.WebhookURL == "" {
		return nil
	}
	if len(c.WebhookURL) > maxWebhookURLBytes {
		return errors.New("notification: webhook URL is too long")
	}
	parsed, err := url.ParseRequestURI(c.WebhookURL)
	if err != nil || parsed.Hostname() == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("notification: webhook URL must be a valid HTTPS URL")
	}
	if !allowedWebhookHost(c.Type, parsed.Hostname()) {
		return errors.New("notification: webhook host is not approved for the selected platform")
	}
	// 通用 Webhook 只发公网地址：直接填了内网、回环 IP 或 localhost 的，添加时就拒绝；域名解析到内网的在发送时由 netguard 拒绝。
	if c.Type == ChannelTypeWebhook {
		host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && !netguard.IsPublicIP(ip)) {
			return errors.New("notification: 通用 Webhook 的地址必须是公网地址")
		}
	}
	if c.Type == ChannelTypeDingTalk && parsed.Query().Get("access_token") == "" {
		return errors.New("notification: DingTalk webhook URL must carry access_token")
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
	case ChannelTypeDingTalk:
		return host == "oapi.dingtalk.com"
	case ChannelTypeWebhook:
		// 通用 Webhook 不限域名；发送时由 netguard 拒绝解析到非公网地址的主机。
		return host != ""
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
	case ChannelTypeDingTalk:
		return newDingTalkSender(config.WebhookURL, config.Secret, nil)
	case ChannelTypeWebhook:
		return newWebhookSender(config.WebhookURL, config.Secret, nil)
	default:
		return nil, errors.New("notification: unsupported channel type")
	}
}

type noopSender struct{}

func (noopSender) Send(_ context.Context, _ Message) error { return nil }
