package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/notification"
)

type recordingPresenceSender struct{ messages []notification.Message }

func (s *recordingPresenceSender) Send(_ context.Context, message notification.Message) error {
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
	if sender.messages[0].Severity != notification.SeverityCritical || sender.messages[1].Severity != notification.SeverityInfo {
		t.Fatalf("severities = %s, %s", sender.messages[0].Severity, sender.messages[1].Severity)
	}
}

func hostPresenceTransitionSinkProvider(provider func(context.Context) (notification.Sender, error)) hostmetrics.PresenceTransitionFunc {
	return hostPresenceTransitionSinkProviderWithFailure(provider, nil)
}

func hostPresenceTransitionSink(sender notification.Sender) hostmetrics.PresenceTransitionFunc {
	return hostPresenceTransitionSinkProvider(func(context.Context) (notification.Sender, error) { return sender, nil })
}

type flakyPresenceSender struct {
	recorded []notification.Message
	onFail   func()
	failed   bool
}

func (s *flakyPresenceSender) Send(_ context.Context, message notification.Message) error {
	if message.Labels["to"] == hostmetrics.PresenceUnreachable && !s.failed {
		s.failed = true
		s.onFail()
		return errors.New("wecom unavailable")
	}
	s.recorded = append(s.recorded, message)
	return nil
}

// 离线通知首次发送失败、退避期间主机恢复：旧离线通知的内部重试必须被取代，恢复通知之后不能再收到“失联”。
func TestPresenceGateDropsOfflineRetryAfterRecovery(t *testing.T) {
	var statusMu sync.Mutex
	status := hostmetrics.PresenceUnreachable
	setStatus := func(v string) { statusMu.Lock(); status = v; statusMu.Unlock() }
	gate := newPresenceGate(func(context.Context, string) (string, error) {
		statusMu.Lock()
		defer statusMu.Unlock()
		return status, nil
	})
	var sender *flakyPresenceSender
	recovered := make(chan struct{})
	sender = &flakyPresenceSender{onFail: func() {
		// 发送失败的同时，主机恢复上报并发起恢复通知（会等待同一主机的投递锁）
		setStatus(hostmetrics.PresenceReachable)
		go func() {
			defer close(recovered)
			sink := hostPresenceTransitionSinkWithGate(func(context.Context) (notification.Sender, error) { return sender, nil }, nil, gate)
			_ = sink(context.Background(), hostmetrics.PresenceTransition{AgentID: "agent-a", From: hostmetrics.PresenceUnreachable, To: hostmetrics.PresenceReachable, ObservedAt: time.Now()})
		}()
	}}
	sink := hostPresenceTransitionSinkWithGate(func(context.Context) (notification.Sender, error) { return sender, nil }, nil, gate)
	err := sink(context.Background(), hostmetrics.PresenceTransition{AgentID: "agent-a", From: hostmetrics.PresenceReachable, To: hostmetrics.PresenceUnreachable, ObservedAt: time.Now()})
	if err != nil {
		t.Fatalf("offline sink error = %v", err)
	}
	<-recovered
	if len(sender.recorded) != 1 || sender.recorded[0].Labels["to"] != hostmetrics.PresenceReachable {
		t.Fatalf("recorded = %+v, want only the recovery notice", sender.recorded)
	}
}
