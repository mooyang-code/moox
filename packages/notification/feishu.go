package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FeishuSender struct {
	client     *http.Client
	timeout    time.Duration
	webhookURL string
	secret     string
	now        func() time.Time
}

// NewFeishuSender 创建飞书发送器。secret 是机器人安全设置里“签名校验”的密钥，没有启用时留空。
func NewFeishuSender(webhookURL, secret string) (*FeishuSender, error) {
	parsed, err := url.ParseRequestURI(webhookURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || !allowedWebhookHost(ChannelTypeFeishu, parsed.Hostname()) {
		return nil, errors.New("notification: invalid Feishu webhook URL")
	}
	return newFeishuSender(webhookURL, secret, &http.Client{
		Timeout: defaultTimeout,
		// Never follow a redirect for a webhook: the redirected host could
		// receive the alert payload outside the configured trust boundary.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	})
}

func newFeishuSender(webhookURL, secret string, client *http.Client) (*FeishuSender, error) {
	if client == nil {
		client = &http.Client{}
	} else {
		copy := *client
		client = &copy
	}
	client.Timeout = defaultTimeout
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &FeishuSender{client: client, timeout: defaultTimeout, webhookURL: webhookURL, secret: secret, now: time.Now}, nil
}

func (s *FeishuSender) Send(ctx context.Context, message Message) error {
	if s == nil {
		return errors.New("notification: Feishu sender is nil")
	}
	if err := message.validate(); err != nil {
		return err
	}
	request := struct {
		// 启用签名校验时带上秒级时间戳与签名；飞书要求时间戳与当前时间相差不超过 1 小时。
		Timestamp string `json:"timestamp,omitempty"`
		Sign      string `json:"sign,omitempty"`
		MsgType   string `json:"msg_type"`
		Content   struct {
			Text string `json:"text"`
		} `json:"content"`
	}{MsgType: "text"}
	request.Content.Text = renderText(message)
	if s.secret != "" {
		request.Timestamp = strconv.FormatInt(s.now().Unix(), 10)
		request.Sign = feishuSign(request.Timestamp, s.secret)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return errors.New("notification: failed to encode Feishu request")
	}
	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("notification: failed to create Feishu request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			return fmt.Errorf("notification: Feishu request failed: %w", reqCtx.Err())
		}
		return errors.New("notification: Feishu request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("notification: failed to read Feishu response")
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("notification: Feishu response body exceeds %d byte limit", maxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("notification: Feishu returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code != 0 {
		if err != nil {
			return errors.New("notification: invalid Feishu response")
		}
		// 带上飞书给的原因（例如验签失败、关键词不符），“推送失败”事件里才看得出为什么。
		return fmt.Errorf("notification: Feishu returned code %d: %s", result.Code, truncateRunes(result.Msg, 200))
	}
	return nil
}

// feishuSign 按飞书自定义机器人“签名校验”的规则计算签名：
// 以“秒级时间戳 + 换行 + 密钥”作为 HMAC-SHA256 的 key，对空内容求摘要，再 base64。
func feishuSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func renderText(message Message) string {
	text := "[" + localizedSeverity(message.Severity) + "]"
	if message.Title != "" {
		text += " " + message.Title
	}
	if message.Body != "" {
		text += "\n" + message.Body
	}
	if len(message.Labels) > 0 {
		keys := make([]string, 0, len(message.Labels))
		for key := range message.Labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		labels := make([]string, 0, len(keys))
		for _, key := range keys {
			labels = append(labels, key+"="+message.Labels[key])
		}
		text += "\n" + strings.Join(labels, ", ")
	}
	if message.Key != "" {
		text += "\n告警标识: " + message.Key
	}
	return text
}
