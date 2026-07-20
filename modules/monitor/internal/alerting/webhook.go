package alerting

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

type WebhookNotifier struct {
	Client  *http.Client
	Timeout time.Duration
}

func (n WebhookNotifier) Send(ctx context.Context, webhook domain.WebhookChannel, event Event) error {
	method := webhook.Method
	if method == "" {
		method = http.MethodPost
	}
	body := renderTemplate(webhook.BodyTemplate, event)
	req, err := http.NewRequestWithContext(ctx, method, webhook.URL, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	for k, v := range parseHeaders(webhook.Headers) {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := n.Client
	if client == nil {
		timeout := n.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func renderTemplate(tpl string, event Event) string {
	if tpl == "" {
		tpl = `{"check_id":"{{check_id}}","status":"{{status}}","event_type":"{{event_type}}","error_message":"{{error_message}}"}`
	}
	replacements := map[string]string{
		"{{check_id}}":      jsonStringValue(event.Check.CheckID),
		"{{check_name}}":    jsonStringValue(event.Check.Name),
		"{{group_name}}":    jsonStringValue(event.Check.GroupName),
		"{{status}}":        jsonStringValue(event.Status),
		"{{event_type}}":    jsonStringValue(event.EventType),
		"{{target}}":        jsonStringValue(eventTarget(event.Check)),
		"{{latency_ms}}":    fmt.Sprintf("%d", event.Result.LatencyMS),
		"{{error_message}}": jsonStringValue(event.Result.ErrorMessage),
		"{{dedupe_key}}":    jsonStringValue(event.DedupeKey),
		"{{instance_id}}":   jsonStringValue(event.Result.InstanceID),
		"{{checked_at}}":    jsonStringValue(event.Result.CheckedAt.UTC().Format(time.RFC3339Nano)),
	}
	for old, newValue := range replacements {
		tpl = strings.ReplaceAll(tpl, old, newValue)
	}
	return tpl
}

func jsonStringValue(value string) string {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) < 2 {
		return value
	}
	return string(raw[1 : len(raw)-1])
}

func parseHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func eventTarget(check domain.Check) string {
	if check.Kind == domain.CheckKindTCP {
		return fmt.Sprintf("%s:%d", check.TCPHost, check.TCPPort)
	}
	return check.URL
}

func newEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "event-" + hex.EncodeToString(b[:])
	}
	return "event-" + time.Now().UTC().Format("20060102150405.000000000")
}
