package alerting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
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
	check := domain.Check{SpaceID: "crypto_market", CheckID: "collector:market", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:market", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	evaluator := NewEvaluator(repos.Alerts, Options{
		Channel: func(context.Context) (*domain.NotificationChannel, error) {
			return &domain.NotificationChannel{ChannelType: "wecom", WebhookURL: "https://not-approved.example/hook"}, nil
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
	check := domain.Check{SpaceID: "crypto_market", CheckID: "collector:recovery", Name: "行情采集", Enabled: true}
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:collector:recovery", CheckID: check.CheckID, FailureThreshold: 1, SuccessThreshold: 1, SendOnResolved: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	evaluator := NewEvaluator(repos.Alerts, Options{
		Channel: func(context.Context) (*domain.NotificationChannel, error) {
			return &domain.NotificationChannel{ChannelType: "wecom", WebhookURL: "https://not-approved.example/hook"}, nil
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
