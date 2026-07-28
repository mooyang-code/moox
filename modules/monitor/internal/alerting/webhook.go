package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/msgbox"
)

type WebhookNotifier struct {
	NewSender func(string) (msgbox.Sender, error)
	Timeout   time.Duration
}

func (n WebhookNotifier) Send(ctx context.Context, webhook domain.WebhookChannel, event Event) error {
	factory := n.NewSender
	if factory == nil {
		factory = func(url string) (msgbox.Sender, error) {
			if n.Timeout > 0 {
				return msgbox.NewWeComSenderWithTimeout(url, n.Timeout)
			}
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

func newEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "event-" + hex.EncodeToString(b[:])
	}
	return "event-" + time.Now().UTC().Format("20060102150405.000000000")
}
