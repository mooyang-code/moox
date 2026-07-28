package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
)

func TestHardDeleteAllowsRepeatedRecreateAndCleansResults(t *testing.T) {
	ctx := context.Background()
	repos := openTestDB(t).Repositories()
	for cycle := 0; cycle < 2; cycle++ {
		check := testCheck("space-a", "check-a", true)
		if err := repos.Checks.Create(ctx, check); err != nil {
			t.Fatalf("cycle %d create: %v", cycle, err)
		}
		if err := repos.Results.Insert(ctx, &domain.CheckResult{
			ResultID: "result-" + string(rune('a'+cycle)), SpaceID: "space-a", CheckID: "check-a",
			InstanceID: "monitor", Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("cycle %d result: %v", cycle, err)
		}
		if err := repos.Checks.Delete(ctx, "space-a", "check-a"); err != nil {
			t.Fatalf("cycle %d delete: %v", cycle, err)
		}
		if _, err := repos.Checks.Get(ctx, "space-a", "check-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("cycle %d get error=%v", cycle, err)
		}
		rows, err := repos.Results.Recent(ctx, "space-a", "check-a", 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("cycle %d results=%d err=%v", cycle, len(rows), err)
		}
	}
}

func TestDeleteRejectsReferencedCheckAndWebhook(t *testing.T) {
	ctx := context.Background()
	repos := openTestDB(t).Repositories()
	if err := repos.Checks.Create(ctx, testCheck("space-a", "check-a", true)); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateWebhook(ctx, testWebhook("space-a", "ops", true)); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, testAlertRule("space-a", "rule-a", "check-a", "ops")); err != nil {
		t.Fatal(err)
	}
	if err := repos.Checks.Delete(ctx, "space-a", "check-a"); !errors.Is(err, ErrResourceReferenced) {
		t.Fatalf("delete referenced check error=%v", err)
	}
	if err := repos.Alerts.DeleteWebhook(ctx, "space-a", "ops"); !errors.Is(err, ErrResourceReferenced) {
		t.Fatalf("delete referenced webhook error=%v", err)
	}
}

func TestAlertConfigHardDeleteAllowsRepeatedRecreate(t *testing.T) {
	ctx := context.Background()
	repos := openTestDB(t).Repositories()
	if err := repos.Checks.Create(ctx, testCheck("space-a", "check-a", true)); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		if err := repos.Alerts.CreateWebhook(ctx, testWebhook("space-a", "ops", true)); err != nil {
			t.Fatalf("cycle %d create webhook: %v", cycle, err)
		}
		if err := repos.Alerts.CreateRule(ctx, testAlertRule("space-a", "rule-a", "check-a", "ops")); err != nil {
			t.Fatalf("cycle %d create rule: %v", cycle, err)
		}
		if err := repos.Alerts.DeleteRule(ctx, "space-a", "rule-a"); err != nil {
			t.Fatalf("cycle %d delete rule: %v", cycle, err)
		}
		if err := repos.Alerts.DeleteWebhook(ctx, "space-a", "ops"); err != nil {
			t.Fatalf("cycle %d delete webhook: %v", cycle, err)
		}
	}
}

func TestAlertRuleValidatesEnabledReferencesAndDeletesStateOnly(t *testing.T) {
	ctx := context.Background()
	repos := openTestDB(t).Repositories()
	if err := repos.Alerts.CreateWebhook(ctx, testWebhook("space-a", "ops", true)); err != nil {
		t.Fatal(err)
	}
	rule := testAlertRule("space-a", "rule-a", "missing", "ops")
	if err := repos.Alerts.CreateRule(ctx, rule); err == nil {
		t.Fatal("rule with missing check was accepted")
	}
	if err := repos.Checks.Create(ctx, testCheck("space-a", "check-a", false)); err != nil {
		t.Fatal(err)
	}
	rule.CheckID = "check-a"
	if err := repos.Alerts.CreateRule(ctx, rule); err == nil {
		t.Fatal("rule with disabled check was accepted")
	}
	check := testCheck("space-a", "check-a", true)
	if err := repos.Checks.Update(ctx, check); err != nil {
		t.Fatal(err)
	}
	webhook := testWebhook("space-a", "ops", false)
	if err := repos.Alerts.UpdateWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, rule); err == nil {
		t.Fatal("rule with disabled webhook was accepted")
	}
	webhook.Enabled = true
	if err := repos.Alerts.UpdateWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	webhook.Enabled = false
	if err := repos.Alerts.UpdateWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.UpdateRule(ctx, rule); err == nil {
		t.Fatal("rule update with disabled webhook was accepted")
	}
	webhook.Enabled = true
	if err := repos.Alerts.UpdateWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.UpsertState(ctx, &domain.AlertState{SpaceID: "space-a", RuleID: "rule-a", CheckID: "check-a", Status: domain.AlertStatusFiring}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: "history", SpaceID: "space-a", RuleID: "rule-a", CheckID: "check-a", EventType: domain.AlertEventTriggered, Status: domain.AlertStatusFiring, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Alerts.DeleteRule(ctx, "space-a", "rule-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Alerts.GetState(ctx, "space-a", "rule-a", "check-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("state survived delete: %v", err)
	}
	events, err := repos.Alerts.ListEvents(ctx, "space-a", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("history events=%d err=%v", len(events), err)
	}
}

func testCheck(spaceID, checkID string, enabled bool) *domain.Check {
	return &domain.Check{
		SpaceID: spaceID, CheckID: checkID, Name: checkID, Kind: domain.CheckKindHTTP,
		URL: "http://127.0.0.1/healthz", Method: "GET", Headers: "{}", ExpectedStatus: "200-299",
		IntervalSeconds: 30, TimeoutMS: 1000, Enabled: enabled, Source: domain.CheckSourceManual, Labels: "{}",
	}
}

func testWebhook(spaceID, webhookID string, enabled bool) *domain.WebhookChannel {
	return &domain.WebhookChannel{
		SpaceID: spaceID, WebhookID: webhookID, Name: webhookID, URL: "http://127.0.0.1/webhook",
		Method: "POST", Headers: "{}", BodyTemplate: "{}", Enabled: enabled,
	}
}

func testAlertRule(spaceID, ruleID, checkID, webhookID string) *domain.AlertRule {
	return &domain.AlertRule{
		SpaceID: spaceID, RuleID: ruleID, CheckID: checkID, WebhookID: webhookID,
		FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
	}
}

func TestRepositoriesRoundTrip(t *testing.T) {
	ctx := context.Background()
	mgr := openTestDB(t)
	repos := mgr.Repositories()
	checks := repos.Checks
	results := repos.Results
	alerts := repos.Alerts

	dueAt := time.Now().Add(-time.Minute)
	check := &domain.Check{
		SpaceID:         "space-a",
		CheckID:         "check-a",
		Name:            "API health",
		GroupName:       "api",
		Kind:            domain.CheckKindHTTP,
		URL:             "http://127.0.0.1/healthz",
		Method:          "GET",
		Headers:         "{}",
		IntervalSeconds: 30,
		TimeoutMS:       1000,
		ExpectedStatus:  "200-299",
		Enabled:         true,
		Source:          domain.CheckSourceManual,
		Labels:          "{}",
		NextCheckAt:     &dueAt,
	}
	if err := checks.Create(ctx, check); err != nil {
		t.Fatalf("create check: %v", err)
	}
	check.Name = "API health updated"
	if err := checks.Update(ctx, check); err != nil {
		t.Fatalf("update check: %v", err)
	}
	got, err := checks.Get(ctx, "space-a", "check-a")
	if err != nil {
		t.Fatalf("get check: %v", err)
	}
	if got.Name != "API health updated" {
		t.Fatalf("check name = %q", got.Name)
	}
	list, err := checks.List(ctx, ListChecksOptions{SpaceID: "space-a"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list checks len=%d err=%v", len(list), err)
	}
	due, err := checks.ListDue(ctx, time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("list due len=%d err=%v", len(due), err)
	}

	now := time.Now()
	if err := results.Insert(ctx, &domain.CheckResult{
		ResultID:   "result-a",
		SpaceID:    "space-a",
		CheckID:    "check-a",
		InstanceID: "monitor-a",
		Success:    true,
		Status:     domain.CheckStatusOK,
		HTTPStatus: 200,
		Connected:  true,
		LatencyMS:  12,
		CheckedAt:  now,
	}); err != nil {
		t.Fatalf("insert result: %v", err)
	}
	recent, err := results.Recent(ctx, "space-a", "check-a", 10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent len=%d err=%v", len(recent), err)
	}

	if err := alerts.CreateWebhook(ctx, &domain.WebhookChannel{
		SpaceID:      "space-a",
		WebhookID:    "webhook-a",
		Name:         "ops",
		URL:          "http://127.0.0.1/webhook",
		Method:       "POST",
		Headers:      "{}",
		BodyTemplate: "{}",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	webhooks, err := alerts.ListWebhooks(ctx, "space-a")
	if err != nil || len(webhooks) != 1 {
		t.Fatalf("webhooks len=%d err=%v", len(webhooks), err)
	}
	if err := alerts.CreateRule(ctx, &domain.AlertRule{
		SpaceID:                        "space-a",
		RuleID:                         "rule-a",
		CheckID:                        "check-a",
		WebhookID:                      "webhook-a",
		FailureThreshold:               3,
		SuccessThreshold:               2,
		MinimumReminderIntervalSeconds: 300,
		SendOnResolved:                 true,
		Enabled:                        true,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	rules, err := alerts.ListEnabledRulesForCheck(ctx, "space-a", "check-a")
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules len=%d err=%v", len(rules), err)
	}
	if err := alerts.CreateRule(ctx, &domain.AlertRule{
		SpaceID:          "space-a",
		RuleID:           "rule-disabled",
		CheckID:          "check-a",
		WebhookID:        "webhook-a",
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Enabled:          false,
	}); err != nil {
		t.Fatalf("create disabled rule: %v", err)
	}
	allRulesForCheck, err := alerts.ListRulesForCheck(ctx, "space-a", "check-a")
	if err != nil || len(allRulesForCheck) != 2 {
		t.Fatalf("all rules for check len=%d err=%v", len(allRulesForCheck), err)
	}
	if err := alerts.UpsertState(ctx, &domain.AlertState{
		SpaceID:      "space-a",
		RuleID:       "rule-a",
		CheckID:      "check-a",
		Status:       domain.AlertStatusFiring,
		FailureCount: 3,
		DedupeKey:    "rule-a:check-a",
	}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}
	state, err := alerts.GetState(ctx, "space-a", "rule-a", "check-a")
	if err != nil || state.Status != domain.AlertStatusFiring {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	old := now.Add(-48 * time.Hour)
	if err := results.Insert(ctx, &domain.CheckResult{
		ResultID:   "result-old",
		SpaceID:    "space-a",
		CheckID:    "check-a",
		InstanceID: "monitor-a",
		Success:    true,
		Status:     domain.CheckStatusOK,
		CheckedAt:  old,
	}); err != nil {
		t.Fatalf("insert old result: %v", err)
	}
	removedResults, err := results.DeleteOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil || removedResults != 1 {
		t.Fatalf("removed results=%d err=%v", removedResults, err)
	}
	if err := alerts.CreateEvent(ctx, &domain.AlertEvent{
		EventID:   "event-old",
		SpaceID:   "space-a",
		RuleID:    "rule-a",
		CheckID:   "check-a",
		EventType: domain.AlertEventTriggered,
		Status:    domain.AlertStatusFiring,
		CreatedAt: old,
	}); err != nil {
		t.Fatalf("create old event: %v", err)
	}
	removedEvents, err := alerts.DeleteEventsOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil || removedEvents != 1 {
		t.Fatalf("removed events=%d err=%v", removedEvents, err)
	}

	for _, ruleID := range []string{"rule-a", "rule-disabled"} {
		if err := alerts.DeleteRule(ctx, "space-a", ruleID); err != nil {
			t.Fatalf("delete alert rule %s: %v", ruleID, err)
		}
	}
	if err := checks.Delete(ctx, "space-a", "check-a"); err != nil {
		t.Fatalf("delete check: %v", err)
	}
	list, err = checks.List(ctx, ListChecksOptions{SpaceID: "space-a"})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("hard-deleted check still listed")
	}
}

func openTestDB(t *testing.T) *Store {
	t.Helper()

	mgr, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return mgr
}
