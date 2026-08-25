package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type FeishuSender struct {
	client     *http.Client
	timeout    time.Duration
	webhookURL string
}

func NewFeishuSender(webhookURL string) (*FeishuSender, error) {
	parsed, err := url.ParseRequestURI(webhookURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || !allowedWebhookHost(ChannelTypeFeishu, parsed.Hostname()) {
		return nil, errors.New("notification: invalid Feishu webhook URL")
	}
	return newFeishuSender(webhookURL, &http.Client{
		Timeout: defaultTimeout,
		// Never follow a redirect for a webhook: the redirected host could
		// receive the alert payload outside the configured trust boundary.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	})
}

func newFeishuSender(webhookURL string, client *http.Client) (*FeishuSender, error) {
	if client == nil {
		client = &http.Client{}
	} else {
		copy := *client
		client = &copy
	}
	client.Timeout = defaultTimeout
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &FeishuSender{client: client, timeout: defaultTimeout, webhookURL: webhookURL}, nil
}

func (s *FeishuSender) Send(ctx context.Context, message Message) error {
	if s == nil {
		return errors.New("notification: Feishu sender is nil")
	}
	if err := message.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}{MsgType: "text", Content: struct {
		Text string `json:"text"`
	}{Text: renderText(message)}})
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
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code != 0 {
		if err != nil {
			return errors.New("notification: invalid Feishu response")
		}
		return fmt.Errorf("notification: Feishu returned code %d", result.Code)
	}
	return nil
}

func renderText(message Message) string {
	text := "[" + string(message.Severity) + "]"
	if message.Title != "" {
		text += " " + message.Title
	}
	if message.Key != "" {
		text += "\n告警标识: " + message.Key
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
	return text
}
