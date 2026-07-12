package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestRepositoriesRoundTrip(t *testing.T) {
	ctx := context.Background()
	mgr := openTestDB(t)
	checks := NewCheckRepository(mgr.DB())
	results := NewResultRepository(mgr.DB())
	alerts := NewAlertRepository(mgr.DB())
	peers := NewPeerRepository(mgr.DB())

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
		SpaceID:         "space-a",
		RuleID:          "rule-a",
		CheckID:         "check-a",
		Status:          domain.AlertStatusFiring,
		FailureCount:    3,
		OwnerInstanceID: "monitor-a",
		DedupeKey:       "rule-a:check-a",
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

	seenAt := time.Now()
	if err := peers.UpsertInstance(ctx, &domain.MonitorInstance{
		InstanceID: "monitor-a",
		BaseURL:    "http://127.0.0.1:11409",
		Status:     domain.InstanceStatusActive,
		LastSeenAt: &seenAt,
		Snapshot:   "{}",
		IsLocal:    true,
	}); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}
	if err := peers.UpsertSnapshot(ctx, &domain.PeerSnapshot{
		InstanceID: "monitor-b",
		BaseURL:    "http://127.0.0.1:11419",
		Status:     domain.InstanceStatusActive,
		Snapshot:   "{}",
		CheckedAt:  seenAt,
	}); err != nil {
		t.Fatalf("upsert snapshot: %v", err)
	}
	instances, err := peers.ListInstances(ctx)
	if err != nil || len(instances) != 1 {
		t.Fatalf("instances len=%d err=%v", len(instances), err)
	}
	snapshots, err := peers.ListSnapshots(ctx)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots len=%d err=%v", len(snapshots), err)
	}

	if err := checks.Delete(ctx, "space-a", "check-a"); err != nil {
		t.Fatalf("delete check: %v", err)
	}
	list, err = checks.List(ctx, ListChecksOptions{SpaceID: "space-a"})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("soft-deleted check still listed")
	}
}

func openTestDB(t *testing.T) *Manager {
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
