package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// dingTalkKeyword 固定出现在每条钉钉消息的标题和正文开头：机器人的安全设置选“自定义关键词”时，关键词填它即可。
const dingTalkKeyword = "MooX"

// DingTalkSender 向钉钉群的自定义机器人发送 markdown 消息。
type DingTalkSender struct {
	client     *http.Client
	webhookURL string
	secret     string
	now        func() time.Time
}

// NewDingTalkSender 创建钉钉发送器。secret 是机器人安全设置里“加签”的密钥，没有启用加签时留空。
func NewDingTalkSender(webhookURL, secret string) (*DingTalkSender, error) {
	return newDingTalkSender(webhookURL, secret, nil)
}

// newDingTalkSender 的 client 非空时只用于测试（地址不再限定钉钉域名）。
func newDingTalkSender(webhookURL, secret string, client *http.Client) (*DingTalkSender, error) {
	if client == nil {
		if err := (ChannelConfig{Type: ChannelTypeDingTalk, WebhookURL: webhookURL, Secret: secret}).Validate(); err != nil {
			return nil, err
		}
	}
	if webhookURL == "" {
		return nil, errors.New("notification: DingTalk webhook URL is required")
	}
	return &DingTalkSender{client: noRedirectClient(client), webhookURL: webhookURL, secret: secret, now: time.Now}, nil
}

func (s *DingTalkSender) Send(ctx context.Context, message Message) error {
	if s == nil {
		return errNilSender
	}
	if err := message.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": dingTalkTitle(message), "text": renderDingTalkMarkdown(message)},
	})
	if err != nil {
		return errors.New("notification: failed to encode DingTalk request")
	}
	target, err := s.signedURL()
	if err != nil {
		return err
	}
	status, body, err := postJSON(ctx, s.client, defaultTimeout, "DingTalk", target, nil, payload)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("notification: DingTalk returned HTTP %d", status)
	}
	// 钉钉拒收（关键词不匹配、签名不对、限流）时 HTTP 仍是 200，要看返回体的 errcode。
	var result struct {
		ErrorCode    int    `json:"errcode"`
		ErrorMessage string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return errors.New("notification: invalid DingTalk response")
	}
	if result.ErrorCode != 0 {
		return fmt.Errorf("notification: DingTalk returned errcode %d: %s", result.ErrorCode, truncateRunes(result.ErrorMessage, 200))
	}
	return nil
}

// signedURL 在启用加签时给地址加上 timestamp 与 sign。
// 钉钉的算法：以密钥为 HMAC-SHA256 的 key，对“毫秒时间戳 + 换行 + 密钥”求摘要，摘要先 base64 再做 URL 编码。
func (s *DingTalkSender) signedURL() (string, error) {
	if s.secret == "" {
		return s.webhookURL, nil
	}
	parsed, err := url.Parse(s.webhookURL)
	if err != nil {
		return "", errors.New("notification: invalid DingTalk webhook URL")
	}
	timestamp := strconv.FormatInt(s.now().UnixMilli(), 10)
	query := parsed.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", dingTalkSign(timestamp, s.secret))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// dingTalkSign 返回 base64 后的签名；URL 编码由调用方在拼接查询串时完成。
func dingTalkSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func dingTalkTitle(message Message) string {
	title := dingTalkKeyword + " [" + localizedSeverity(message.Severity) + "]"
	if message.Title != "" {
		title += " " + message.Title
	}
	return title
}

// renderDingTalkMarkdown 排版与企业微信一致。钉钉的 markdown 里单个换行不换行，行与行之间用空行隔开。
func renderDingTalkMarkdown(message Message) string {
	lines := []string{"**" + dingTalkTitle(message) + "**"}
	if message.Body != "" {
		lines = append(lines, strings.Split(message.Body, "\n")...)
	}
	keys := make([]string, 0, len(message.Labels))
	for key := range message.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, "> "+key+": "+message.Labels[key])
	}
	if message.Key != "" {
		lines = append(lines, "告警标识："+message.Key)
	}
	return strings.Join(lines, "\n\n")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}
