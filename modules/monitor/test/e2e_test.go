package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
)

func TestSingleMonitorSchedulesEveryCheckAndOwnsEveryAlert(t *testing.T) {
	ctx := context.Background()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repos := mgr.Repositories()

	for _, id := range []string{"service-a", "service-b"} {
		check := &domain.Check{
			CheckID: id, Name: id, Kind: domain.CheckKindHTTP, URL: "http://127.0.0.1/readyz",
			Method: "GET", Headers: "{}", Labels: "{}", IntervalSeconds: 30, TimeoutMS: 1000,
			ExpectedStatus: "200-299", Enabled: true, Source: domain.CheckSourceManual,
		}
		if err := repos.Checks.Create(ctx, check); err != nil {
			t.Fatal(err)
		}
		if err := repos.Alerts.CreateRule(ctx, &domain.AlertRule{
			RuleID: "rule-" + id, CheckID: id, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	evaluator := alerting.NewEvaluator(repos.Alerts, alerting.Options{})
	sched := scheduler.New(repos, scheduler.Options{
		InstanceID: "monitor-local",
		Runner: failingRunner{},
		OnResult: func(ctx context.Context, check domain.Check, result domain.CheckResult) {
			if err := evaluator.Evaluate(ctx, check, result); err != nil {
				t.Errorf("evaluate %s: %v", check.CheckID, err)
			}
		},
	})
	count, err := sched.RunDueOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("scheduled checks = %d, want 2", count)
	}
	events, err := repos.Alerts.ListRecentEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("alert events = %d, want 2", len(events))
	}

	_, err = store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		for _, table := range []string{"t_monitor_instances", "t_monitor_peer_snapshots"} {
			if db.Migrator().HasTable(table) {
				t.Errorf("single-instance monitor created %s", table)
			}
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
}

type failingRunner struct{}

func (failingRunner) Run(_ context.Context, check domain.Check) domain.CheckResult {
	return domain.CheckResult{
		CheckID: check.CheckID, Status: domain.CheckStatusDown, Success: false,
		ErrorMessage: "synthetic failure", CheckedAt: time.Now().UTC(),
	}
}
