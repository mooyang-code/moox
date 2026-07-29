package alerting

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/msgbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSender struct{ messages []msgbox.Message }

func (s *recordingSender) Send(_ context.Context, message msgbox.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestAlertEvaluatorThresholdReminderAndResolve(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	check := testCheck()
	createAlertFixture(t, mgr.Repositories(), domain.AlertRule{
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
	evaluator := NewEvaluator(mgr.Repositories().Alerts, Options{
		Notifier: notifier,
		Now:      func() time.Time { return now },
	})

	for i := 0; i < 2; i++ {
		if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
			t.Fatalf("evaluate failure %d: %v", i, err)
		}
	}
	if notifier.Count() != 0 {
		t.Fatalf("notifier count = %d, want 0", notifier.Count())
	}
	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
		t.Fatalf("third failure: %v", err)
	}
	if notifier.Count() != 1 || notifier.Events()[0] != domain.AlertEventTriggered {
		t.Fatalf("events = %#v", notifier.Events())
	}

	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
		t.Fatalf("early reminder: %v", err)
	}
	if notifier.Count() != 1 {
		t.Fatalf("early reminder sent count = %d", notifier.Count())
	}
	now = now.Add(301 * time.Second)
	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
		t.Fatalf("late reminder: %v", err)
	}
	if notifier.Count() != 2 || notifier.Events()[1] != domain.AlertEventReminder {
		t.Fatalf("events = %#v", notifier.Events())
	}

	if err := evaluator.Evaluate(ctx, check, okResult(check)); err != nil {
		t.Fatalf("first success: %v", err)
	}
	if notifier.Count() != 2 {
		t.Fatalf("first success sent count = %d", notifier.Count())
	}
	if err := evaluator.Evaluate(ctx, check, okResult(check)); err != nil {
		t.Fatalf("second success: %v", err)
	}
	if notifier.Count() != 3 || notifier.Events()[2] != domain.AlertEventResolved {
		t.Fatalf("events = %#v", notifier.Events())
	}
}

func TestAlertEvaluatorSendOnResolvedFalseAndSendFailure(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	alerts := mgr.Repositories().Alerts
	check := testCheck()
	createAlertFixture(t, mgr.Repositories(), domain.AlertRule{
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
	evaluator := NewEvaluator(mgr.Repositories().Alerts, Options{
		Notifier: notifier,
	})
	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err == nil {
		t.Fatal("notification failure was swallowed")
	}
	events, err := alerts.ListEvents(ctx, "space-a", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0].EventType != domain.AlertEventSendFailed {
		t.Fatalf("events = %+v", events)
	}

	notifier.fail = false
	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
		t.Fatalf("retry trigger: %v", err)
	}
	if err := evaluator.Evaluate(ctx, check, okResult(check)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if notifier.Count() != 2 {
		t.Fatalf("resolved should not send; notifier count = %d", notifier.Count())
	}
	events, _ = alerts.ListEvents(ctx, "space-a", 10)
	if events[0].EventType != domain.AlertEventResolved {
		t.Fatalf("latest event = %+v", events[0])
	}
}

func TestAlertRuleRejectsMissingWebhook(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	alerts := mgr.Repositories().Alerts
	check := testCheck()
	check.Enabled = true
	if err := mgr.Repositories().Checks.Create(ctx, &check); err != nil {
		t.Fatalf("create check: %v", err)
	}
	if err := alerts.CreateRule(ctx, &domain.AlertRule{
		SpaceID: "space-a", RuleID: "local-rule", CheckID: check.CheckID,
		FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
	}); err == nil {
		t.Fatal("alert rule without an enabled webhook was accepted")
	}
}

func TestAlertEvaluatorDoesNotDelegateToPeerOwner(t *testing.T) {
	ctx := context.Background()
	mgr := openAlertDB(t)
	alerts := mgr.Repositories().Alerts
	check := testCheck()
	createAlertFixture(t, mgr.Repositories(), domain.AlertRule{
		SpaceID: "space-a", RuleID: "single-instance", CheckID: check.CheckID,
		WebhookID: "webhook-a", FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
	})

	notifier := &recordingNotifier{}
	evaluator := NewEvaluator(alerts, Options{Notifier: notifier})
	if err := evaluator.Evaluate(ctx, check, failedResult(check)); err != nil {
		t.Fatal(err)
	}
	if notifier.Count() != 1 {
		t.Fatalf("single-instance evaluator delegated alert to a peer; sends=%d", notifier.Count())
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

func createAlertFixture(t *testing.T, repos *store.Repositories, rule domain.AlertRule) {
	t.Helper()
	ctx := context.Background()
	check := testCheck()
	check.Enabled = true
	check.IntervalSeconds = 30
	check.TimeoutMS = 1000
	check.Method = "GET"
	check.Headers = "{}"
	check.ExpectedStatus = "200-299"
	check.Source = domain.CheckSourceManual
	check.Labels = "{}"
	if err := repos.Checks.Create(ctx, &check); err != nil {
		t.Fatalf("create check: %v", err)
	}
	if err := repos.Alerts.CreateWebhook(ctx, &domain.WebhookChannel{
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
	if err := repos.Alerts.CreateRule(ctx, &rule); err != nil {
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

func openAlertDB(t *testing.T) *store.Store {
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

func TestWebhookNotifierRoutesThroughMsgboxSender(t *testing.T) {
	sender := &recordingSender{}
	notifier := WebhookNotifier{NewSender: func(string) (msgbox.Sender, error) { return sender, nil }}
	err := notifier.Send(context.Background(), domain.WebhookChannel{URL: "https://example.invalid"}, Event{
		Check: testCheck(), Status: domain.AlertStatusFiring, EventType: domain.AlertEventTriggered,
		Result: failedResult(testCheck()),
	})
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if len(sender.messages) != 1 || sender.messages[0].Severity != msgbox.SeverityCritical {
		t.Fatalf("messages = %#v", sender.messages)
	}
}

func TestWebhookNotifierExplainsKnownFailuresInChinese(t *testing.T) {
	sender := &recordingSender{}
	notifier := WebhookNotifier{NewSender: func(string) (msgbox.Sender, error) { return sender, nil }}
	check := domain.Check{
		CheckID: "sysdeploy:control:storage-primary",
		Name:    "storage-primary@control",
		Source:  domain.CheckSourceSysDeploy,
	}
	err := notifier.Send(context.Background(), domain.WebhookChannel{URL: "https://example.invalid"}, Event{
		Check: check, Status: domain.AlertStatusFiring, EventType: domain.AlertEventReminder,
		Result: domain.CheckResult{
			InstanceID: "monitor-control", ErrorMessage: "unexpected HTTP status 401",
			HTTPStatus: 401, CheckedAt: time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC),
		},
	})
	require.NoError(t, err)
	require.Len(t, sender.messages, 1)
	message := sender.messages[0]
	assert.Equal(t, "持续告警：storage-primary@control", message.Title)
	assert.Contains(t, message.Body, "异常原因：目标服务拒绝了健康检查鉴权（HTTP 401）")
	assert.Contains(t, message.Body, "建议处理：检查 Monitor 与 storage-primary 使用的 health-auth 密钥是否一致")
	assert.Contains(t, message.Body, "检查时间：2026-07-29 17:30:00 CST")
	assert.Equal(t, "持续告警", message.Labels["事件"])
	assert.Equal(t, "异常中", message.Labels["状态"])
}

func TestLocalizedReasonExplainsMarketCanaryStorageRejection(t *testing.T) {
	reason, action := localizedReason(
		domain.Check{CheckID: "market_canary:binance_spot_kline:BTC-USDT:1m"},
		domain.CheckResult{ErrorMessage: "storage_rejected_query:7:dataset disabled"},
	)
	assert.Equal(t, "Storage 拒绝了行情查询请求（返回码 7：dataset disabled）", reason)
	assert.Contains(t, action, "Dataset、Symbol、Frequency")
}
