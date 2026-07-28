package msgbox

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

const (
	defaultTimeout   = 10 * time.Second
	maxResponseBytes = 64 << 10
)

type WeComSender struct {
	client     *http.Client
	timeout    time.Duration
	webhookURL string
}

type weComOption func(*weComOptions) error

type weComOptions struct {
	client    *http.Client
	timeout   time.Duration
	allowHTTP bool
}

func NewWeComSender(webhookURL string) (*WeComSender, error) {
	return newWeComSender(webhookURL)
}

func NewWeComSenderWithTimeout(webhookURL string, timeout time.Duration) (*WeComSender, error) {
	if timeout <= 0 {
		return nil, errors.New("msgbox: WeCom timeout must be positive")
	}
	return newWeComSender(webhookURL, func(options *weComOptions) error {
		options.timeout = timeout
		return nil
	})
}

func newWeComSender(webhookURL string, options ...weComOption) (*WeComSender, error) {
	config := weComOptions{timeout: defaultTimeout}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("msgbox: WeCom option must not be nil")
		}
		if err := option(&config); err != nil {
			return nil, err
		}
	}
	if err := validateWebhookURL(webhookURL, config.allowHTTP); err != nil {
		return nil, err
	}

	client := config.client
	if client == nil {
		client = &http.Client{}
	} else {
		cloned := *client
		client = &cloned
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &WeComSender{
		client:     client,
		timeout:    config.timeout,
		webhookURL: webhookURL,
	}, nil
}

func (s *WeComSender) Send(ctx context.Context, message Message) error {
	if s == nil {
		return errors.New("msgbox: WeCom sender is nil")
	}
	if err := message.validate(); err != nil {
		return err
	}

	payload, err := json.Marshal(struct {
		MessageType string `json:"msgtype"`
		Markdown    struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}{
		MessageType: "markdown",
		Markdown: struct {
			Content string `json:"content"`
		}{Content: renderMarkdown(message)},
	})
	if err != nil {
		return errors.New("msgbox: failed to encode WeCom request")
	}

	requestContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		s.webhookURL,
		bytes.NewReader(payload),
	)
	if err != nil {
		return errors.New("msgbox: failed to create WeCom request")
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return fmt.Errorf("msgbox: WeCom request failed: %w", contextErr)
		}
		return errors.New("msgbox: WeCom request failed")
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("msgbox: failed to read WeCom response")
	}
	if len(responseBody) > maxResponseBytes {
		return fmt.Errorf("msgbox: WeCom response body exceeds %d byte limit", maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("msgbox: WeCom returned HTTP %d", response.StatusCode)
	}

	var result struct {
		ErrorCode int `json:"errcode"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return errors.New("msgbox: invalid WeCom response")
	}
	if result.ErrorCode != 0 {
		return fmt.Errorf("msgbox: WeCom returned errcode %d", result.ErrorCode)
	}
	return nil
}

func validateWebhookURL(rawURL string, allowHTTP bool) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("msgbox: invalid WeCom webhook URL")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return errors.New("msgbox: WeCom webhook URL must use HTTPS")
	}
	return nil
}

func renderMarkdown(message Message) string {
	var content strings.Builder
	if message.Title != "" {
		fmt.Fprintf(&content, "**[%s] %s**", strings.ToUpper(string(message.Severity)), message.Title)
	}
	if message.Key != "" {
		appendLine(&content, "> key: "+message.Key)
	}
	if message.Body != "" {
		appendLine(&content, message.Body)
	}
	keys := make([]string, 0, len(message.Labels))
	for key := range message.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendLine(&content, fmt.Sprintf("> %s: %s", key, message.Labels[key]))
	}
	return content.String()
}

func appendLine(content *strings.Builder, line string) {
	if content.Len() > 0 {
		content.WriteByte('\n')
	}
	content.WriteString(line)
}
