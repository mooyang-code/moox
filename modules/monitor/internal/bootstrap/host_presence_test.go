package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/msgbox"
)

type recordingPresenceSender struct{ messages []msgbox.Message }

func (s *recordingPresenceSender) Send(_ context.Context, message msgbox.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestHostPresenceTransitionSinkNotifiesFailureAndRecovery(t *testing.T) {
	sender := &recordingPresenceSender{}
	sink := hostPresenceTransitionSink(sender)
	now := time.Now().UTC()
	sink.HandlePresenceTransition(t.Context(), hostmetrics.PresenceTransition{
		AgentID: "agent-a", From: hostmetrics.PresenceReachable, To: hostmetrics.PresenceUnreachable, ObservedAt: now,
	})
	sink.HandlePresenceTransition(t.Context(), hostmetrics.PresenceTransition{
		AgentID: "agent-a", From: hostmetrics.PresenceUnreachable, To: hostmetrics.PresenceReachable, ObservedAt: now,
	})
	if len(sender.messages) != 2 {
		t.Fatalf("messages = %d", len(sender.messages))
	}
	if sender.messages[0].Severity != msgbox.SeverityCritical || sender.messages[1].Severity != msgbox.SeverityInfo {
		t.Fatalf("severities = %s, %s", sender.messages[0].Severity, sender.messages[1].Severity)
	}
}
