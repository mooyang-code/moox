package alerting

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/notification"
)

func TestEvaluatorRecordsNotificationConstructionFailure(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repos := mgr.Repositories()
	ctx := context.Background()
	check := domain.Check{SpaceID: "crypto", CheckID: "collector:market", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:market", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	evaluator := NewEvaluator(repos.Alerts, Options{
		Sender: func(ctx context.Context) (notification.Sender, error) {
			return notifier.Sender(ctx, channelList{{ChannelType: "wecom", WebhookURL: "https://not-approved.example/hook"}})
		},
		Now: func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
	})
	if err := evaluator.Evaluate(ctx, check, domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: false, Status: domain.CheckStatusDown, ErrorMessage: "Timer 不可用", CheckedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected notification construction error")
	}
	events, err := repos.Alerts.ListEvents(ctx, check.SpaceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != domain.AlertEventSendFailed || events[1].EventType != domain.AlertEventTriggered {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Message == "" || events[1].Message == "Timer 不可用" {
		t.Fatalf("alert message was not human-readable: %q", events[1].Message)
	}
	// 概览的告警经过读 payload 里的结构化原因，不解析推送正文
	if events[1].Payload != `{"reason":"Timer 不可用"}` {
		t.Fatalf("alert payload = %s", events[1].Payload)
	}
}

type channelList []domain.NotificationChannel

func (l channelList) List(context.Context) ([]domain.NotificationChannel, error) { return l, nil }

type partialSender struct{ calls int }

func (s *partialSender) Send(context.Context, notification.Message) error {
	s.calls++
	return &notifier.PartialError{Failures: []error{errors.New("飞书（open.feishu.cn …0000）：notification: Feishu returned HTTP 404")}}
}

// 部分渠道失败：消息已送达，只记录失败渠道，不按未送达重试（否则已收到的群每次评估都会重复收到）。
func TestEvaluatorDoesNotRetryWhenSomeChannelDelivered(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repos := mgr.Repositories()
	ctx := context.Background()
	check := domain.Check{SpaceID: "crypto", CheckID: "collector:partial", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:partial", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, MinimumReminderIntervalSeconds: 300, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sender := &partialSender{}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	evaluator := NewEvaluator(repos.Alerts, Options{
		Sender: func(context.Context) (notification.Sender, error) { return sender, nil },
		Now:    func() time.Time { return now },
	})
	failure := domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: false, Status: domain.CheckStatusDown, ErrorMessage: "Timer 不可用", CheckedAt: now}
	if err := evaluator.Evaluate(ctx, check, failure); err != nil {
		t.Fatalf("a partially delivered alert must not fail evaluation: %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := evaluator.Evaluate(ctx, check, failure); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("partially delivered alert was resent %d times", sender.calls)
	}
	events, err := repos.Alerts.ListEvents(ctx, check.SpaceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != domain.AlertEventSendFailed || events[1].EventType != domain.AlertEventTriggered {
		t.Fatalf("events = %+v", events)
	}
}

func TestNotificationSeverityMatchesAlertLifecycle(t *testing.T) {
	if notificationSeverity(domain.AlertEventTriggered) != notification.SeverityCritical {
		t.Fatal("triggered alerts must be critical")
	}
	if notificationSeverity(domain.AlertEventReminder) != notification.SeverityWarning {
		t.Fatal("reminders must be warnings")
	}
	if notificationSeverity(domain.AlertEventResolved) != notification.SeverityInfo {
		t.Fatal("resolved alerts must be informational")
	}
}

func TestRecoveryNotificationFailureKeepsAlertFiringForRetry(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repos := mgr.Repositories()
	ctx := context.Background()
	check := domain.Check{SpaceID: "crypto", CheckID: "collector:recovery", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:recovery", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, SendOnResolved: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	evaluator := NewEvaluator(repos.Alerts, Options{
		Sender: func(ctx context.Context) (notification.Sender, error) {
			return notifier.Sender(ctx, channelList{{ChannelType: "wecom", WebhookURL: "https://not-approved.example/hook"}})
		},
		Now: func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
	})
	failure := domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: false, Status: domain.CheckStatusDown, ErrorMessage: "Timer 不可用", CheckedAt: time.Now().UTC()}
	_ = evaluator.Evaluate(ctx, check, failure)
	recovery := failure
	recovery.Success = true
	recovery.Status = domain.CheckStatusOK
	recovery.ErrorMessage = ""
	if err := evaluator.Evaluate(ctx, check, recovery); err == nil {
		t.Fatal("expected recovery notification failure")
	}
	state, err := repos.Alerts.GetState(ctx, check.SpaceID, "default:collector:recovery", check.CheckID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.AlertStatusFiring || state.ResolvedAt != nil {
		t.Fatalf("recovery failure lost firing state: %+v", state)
	}
}

func TestFailedSendAfterParentContextExpiresClearsReminderSoNextSampleRetries(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repos := mgr.Repositories()
	background := context.Background()
	check := domain.Check{SpaceID: "crypto", CheckID: "collector:market", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(background, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(background, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:market", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(background)
	evaluator := NewEvaluator(repos.Alerts, Options{
		Sender: func(ctx context.Context) (notification.Sender, error) {
			cancel() // 发送期间父 context 到期
			return notifier.Sender(ctx, channelList{{ChannelType: "wecom", WebhookURL: "https://not-approved.example/hook"}})
		},
		Now: func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
	})
	result := domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: false, Status: domain.CheckStatusDown, ErrorMessage: "Timer 不可用", CheckedAt: time.Now().UTC()}
	if err := evaluator.Evaluate(ctx, check, result); err == nil {
		t.Fatal("expected send failure")
	}
	state, err := repos.Alerts.GetState(background, check.SpaceID, "default:collector:market", check.CheckID)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastReminderAt != nil {
		t.Fatalf("发送失败后必须清除提醒时间戳，否则后续采样被抑制一个提醒间隔: %v", state.LastReminderAt)
	}
}
