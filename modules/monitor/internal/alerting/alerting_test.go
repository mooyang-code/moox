package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestAlertEvaluatorThresholdReminderAndResolve(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	alerts := store.NewAlertRepository(mgr.DB())
	check := testCheck()
	createAlertFixture(t, alerts, domain.AlertRule{
		SpaceID:                        "space-a",
		RuleID:                         "rule-a",
		CheckID:                        "check-a",
		WebhookID:                      "webhook-a",
		FailureThreshold:               3,
		SuccessThreshold:               2,
		MinimumReminderIntervalSeconds: 300,
		SendOnResolved:                 true,
		Enabled:                        true,
	})

	now := time.Now()
	notifier := &recordingNotifier{}
	evaluator := NewEvaluator(mgr.DB(), Options{
		InstanceID: "monitor-a",
		Notifier:   notifier,
		Now:        func() time.Time { return now },
	})

	for i := 0; i < 2; i++ {
		if err := evaluator.Evaluate(ctx, check, failedResult(check), nil); err != nil {
			t.Fatalf("evaluate failure %d: %v", i, err)
		}
	}
	if notifier.Count() != 0 {
		t.Fatalf("notifier count = %d, want 0", notifier.Count())
	}
	if err := evaluator.Evaluate(ctx, check, failedResult(check), nil); err != nil {
		t.Fatalf("third failure: %v", err)
	}
	if notifier.Count() != 1 || notifier.Events()[0] != domain.AlertEventTriggered {
		t.Fatalf("events = %#v", notifier.Events())
	}

	if err := evaluator.Evaluate(ctx, check, failedResult(check), nil); err != nil {
		t.Fatalf("early reminder: %v", err)
	}
	if notifier.Count() != 1 {
		t.Fatalf("early reminder sent count = %d", notifier.Count())
	}
	now = now.Add(301 * time.Second)
	if err := evaluator.Evaluate(ctx, check, failedResult(check), nil); err != nil {
		t.Fatalf("late reminder: %v", err)
	}
	if notifier.Count() != 2 || notifier.Events()[1] != domain.AlertEventReminder {
		t.Fatalf("events = %#v", notifier.Events())
	}

	if err := evaluator.Evaluate(ctx, check, okResult(check), nil); err != nil {
		t.Fatalf("first success: %v", err)
	}
	if notifier.Count() != 2 {
		t.Fatalf("first success sent count = %d", notifier.Count())
	}
	if err := evaluator.Evaluate(ctx, check, okResult(check), nil); err != nil {
		t.Fatalf("second success: %v", err)
	}
	if notifier.Count() != 3 || notifier.Events()[2] != domain.AlertEventResolved {
		t.Fatalf("events = %#v", notifier.Events())
	}
}

func TestAlertEvaluatorSendOnResolvedFalseAndSendFailure(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	alerts := store.NewAlertRepository(mgr.DB())
	check := testCheck()
	createAlertFixture(t, alerts, domain.AlertRule{
		SpaceID:          "space-a",
		RuleID:           "rule-a",
		CheckID:          "check-a",
		WebhookID:        "webhook-a",
		FailureThreshold: 1,
		SuccessThreshold: 1,
		SendOnResolved:   false,
		Enabled:          true,
	})

	notifier := &recordingNotifier{fail: true}
	evaluator := NewEvaluator(mgr.DB(), Options{
		InstanceID: "monitor-a",
		Notifier:   notifier,
	})
	if err := evaluator.Evaluate(ctx, check, failedResult(check), nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	events, err := alerts.ListEvents(ctx, "space-a", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0].EventType != domain.AlertEventSendFailed {
		t.Fatalf("events = %+v", events)
	}

	notifier.fail = false
	if err := evaluator.Evaluate(ctx, check, okResult(check), nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if notifier.Count() != 1 {
		t.Fatalf("resolved should not send; notifier count = %d", notifier.Count())
	}
	events, _ = alerts.ListEvents(ctx, "space-a", 10)
	if events[0].EventType != domain.AlertEventResolved {
		t.Fatalf("latest event = %+v", events[0])
	}
}

func TestRenderTemplateEscapesJSONValues(t *testing.T) {
	body := renderTemplate("", Event{
		EventType: domain.AlertEventTriggered,
		Status:    domain.AlertStatusFiring,
		Check: domain.Check{
			CheckID: "check-a",
			Name:    "API \"prod\"",
			Kind:    domain.CheckKindHTTP,
			URL:     "http://example.com/healthz?name=\"api\"",
		},
		Result: domain.CheckResult{
			ErrorMessage: "failed with \"quote\"\nand newline",
			CheckedAt:    time.Now(),
		},
	})
	if !json.Valid([]byte(body)) {
		t.Fatalf("rendered body is invalid JSON: %s", body)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal rendered body: %v", err)
	}
	if payload["error_message"] != "failed with \"quote\"\nand newline" {
		t.Fatalf("error_message = %q", payload["error_message"])
	}
}

type recordingNotifier struct {
	fail   bool
	events []string
}

func (n *recordingNotifier) Send(ctx context.Context, webhook domain.WebhookChannel, event Event) error {
	n.events = append(n.events, event.EventType)
	if n.fail {
		return fmt.Errorf("send failed")
	}
	return nil
}

func (n *recordingNotifier) Count() int {
	return len(n.events)
}

func (n *recordingNotifier) Events() []string {
	return append([]string(nil), n.events...)
}

func createAlertFixture(t *testing.T, alerts *store.AlertRepository, rule domain.AlertRule) {
	t.Helper()
	ctx := context.Background()
	if err := alerts.CreateWebhook(ctx, &domain.WebhookChannel{
		SpaceID:      "space-a",
		WebhookID:    "webhook-a",
		Name:         "Ops",
		URL:          "http://127.0.0.1/webhook",
		Method:       "POST",
		Headers:      "{}",
		BodyTemplate: "{}",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	if err := alerts.CreateRule(ctx, &rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}
}

func testCheck() domain.Check {
	return domain.Check{
		SpaceID: "space-a",
		CheckID: "check-a",
		Name:    "API",
		Kind:    domain.CheckKindHTTP,
		URL:     "http://127.0.0.1/healthz",
	}
}

func failedResult(check domain.Check) domain.CheckResult {
	return domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: false, Status: domain.CheckStatusDown, ErrorMessage: "boom", CheckedAt: time.Now()}
}

func okResult(check domain.Check) domain.CheckResult {
	return domain.CheckResult{SpaceID: check.SpaceID, CheckID: check.CheckID, Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now()}
}

func openAlertDB(t *testing.T) *store.Manager {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return mgr
}
