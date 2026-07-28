package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/msgbox"
)

type WebhookNotifier struct {
	NewSender func(string) (msgbox.Sender, error)
}

func (n WebhookNotifier) Send(ctx context.Context, webhook domain.WebhookChannel, event Event) error {
	factory := n.NewSender
	if factory == nil {
		factory = func(url string) (msgbox.Sender, error) {
			return msgbox.NewWeComSender(url)
		}
	}
	sender, err := factory(webhook.URL)
	if err != nil {
		return err
	}
	severity := msgbox.SeverityCritical
	if event.EventType == domain.AlertEventResolved {
		severity = msgbox.SeverityInfo
	}
	return sender.Send(ctx, msgbox.Message{
		Key: event.DedupeKey, Severity: severity,
		Title: firstText(event.Check.Name, event.Check.CheckID, "MooX alert"),
		Body:  firstText(event.Message, event.Result.ErrorMessage, event.Status),
		Labels: map[string]string{
			"check_id": event.Check.CheckID, "event_type": event.EventType,
			"status": event.Status, "instance_id": event.Result.InstanceID,
		},
	})
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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
