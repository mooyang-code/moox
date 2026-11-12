package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/packages/netguard"
)

const webhookTimeout = 5 * time.Second

// WebhookSender 是通用 Webhook：向一个公网 HTTPS 地址 POST 固定格式的 JSON，返回 2xx 算送达。
//
// 请求体字段：key、severity、title、body、labels、sent_at（UTC，RFC 3339）。
// 配置了密钥时带上 X-MooX-Timestamp（毫秒）与 X-MooX-Signature（见 WebhookSignature），接收方可以据此校验来源。
type WebhookSender struct {
	client     *http.Client
	webhookURL string
	secret     string
	now        func() time.Time
}

// NewWebhookSender 创建通用 Webhook 发送器。secret 选填。
func NewWebhookSender(webhookURL, secret string) (*WebhookSender, error) {
	return newWebhookSender(webhookURL, secret, nil)
}

// newWebhookSender 的 client 非空时只用于测试（不限 HTTPS，也不做地址防护）。
func newWebhookSender(webhookURL, secret string, client *http.Client) (*WebhookSender, error) {
	if client == nil {
		if err := (ChannelConfig{Type: ChannelTypeWebhook, WebhookURL: webhookURL, Secret: secret}).Validate(); err != nil {
			return nil, err
		}
		// 地址由用户在页面上任意填写：只拨号到公网地址，免得被用来探测内网；也不走环境里配置的代理。
		client = &http.Client{Transport: &http.Transport{
			DialContext:           netguard.NewDialContext(netguard.DialOptions{}),
			TLSHandshakeTimeout:   webhookTimeout,
			ResponseHeaderTimeout: webhookTimeout,
			DisableKeepAlives:     true,
		}}
	}
	if webhookURL == "" {
		return nil, errors.New("notification: webhook URL is required")
	}
	return &WebhookSender{client: noRedirectClient(client), webhookURL: webhookURL, secret: secret, now: time.Now}, nil
}

func (s *WebhookSender) Send(ctx context.Context, message Message) error {
	if s == nil {
		return errNilSender
	}
	if err := message.validate(); err != nil {
		return err
	}
	now := s.now()
	payload, err := json.Marshal(struct {
		Key      string            `json:"key"`
		Severity Severity          `json:"severity"`
		Title    string            `json:"title"`
		Body     string            `json:"body"`
		Labels   map[string]string `json:"labels,omitempty"`
		SentAt   string            `json:"sent_at"`
	}{message.Key, message.Severity, message.Title, message.Body, message.Labels, now.UTC().Format(time.RFC3339)})
	if err != nil {
		return errors.New("notification: failed to encode webhook request")
	}
	header := http.Header{}
	if s.secret != "" {
		timestamp := strconv.FormatInt(now.UnixMilli(), 10)
		header.Set("X-MooX-Timestamp", timestamp)
		header.Set("X-MooX-Signature", WebhookSignature(s.secret, timestamp, payload))
	}
	status, _, err := postJSON(ctx, s.client, webhookTimeout, "webhook", s.webhookURL, header, payload)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("notification: webhook returned HTTP %d", status)
	}
	return nil
}
