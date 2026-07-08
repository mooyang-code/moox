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
		"{{check_id}}":      event.Check.CheckID,
		"{{check_name}}":    event.Check.Name,
		"{{group_name}}":    event.Check.GroupName,
		"{{status}}":        event.Status,
		"{{event_type}}":    event.EventType,
		"{{target}}":        eventTarget(event.Check),
		"{{latency_ms}}":    fmt.Sprintf("%d", event.Result.LatencyMS),
		"{{error_message}}": event.Result.ErrorMessage,
		"{{dedupe_key}}":    event.DedupeKey,
		"{{instance_id}}":   event.OwnerInstanceID,
		"{{checked_at}}":    event.Result.CheckedAt.UTC().Format(time.RFC3339Nano),
	}
	for old, newValue := range replacements {
		tpl = strings.ReplaceAll(tpl, old, newValue)
	}
	return tpl
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
