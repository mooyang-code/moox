package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSchedulerRunDueOnce(t *testing.T) {
	ctx := context.Background()
	mgr := openSchedulerDB(t)
	checkRepo := mgr.Repositories().Checks
	resultRepo := mgr.Repositories().Results
	dueAt := time.Now().Add(-time.Second)

	createCheck(t, checkRepo, domain.Check{CheckID: "enabled", Enabled: true, NextCheckAt: &dueAt})
	createCheck(t, checkRepo, domain.Check{CheckID: "disabled", Enabled: false, NextCheckAt: &dueAt})
	createCheck(t, checkRepo, domain.Check{CheckID: "external", Kind: domain.CheckKindExternal, Enabled: true, NextCheckAt: &dueAt})

	s := New(mgr.Repositories(), Options{
		InstanceID:     "monitor-a",
		MaxConcurrency: 2,
		Runner: runnerFunc(func(ctx context.Context, check domain.Check) domain.CheckResult {
			return okResult(check)
		}),
	})
	n, err := s.RunDueOnce(ctx)
	if err != nil {
		t.Fatalf("RunDueOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("run count = %d, want 1", n)
	}
	results, err := resultRepo.Recent(ctx, "", "enabled", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("results len=%d err=%v", len(results), err)
	}
	disabledResults, _ := resultRepo.Recent(ctx, "", "disabled", 10)
	if len(disabledResults) != 0 {
		t.Fatalf("disabled result len = %d", len(disabledResults))
	}
	updated, err := checkRepo.Get(ctx, "", "enabled")
	if err != nil {
		t.Fatalf("get updated check: %v", err)
	}
	if updated.NextCheckAt == nil || updated.NextCheckAt.Before(time.Now()) {
		t.Fatalf("next check not advanced: %+v", updated.NextCheckAt)
	}
}

func TestSchedulerConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	mgr := openSchedulerDB(t)
	checkRepo := mgr.Repositories().Checks
	dueAt := time.Now().Add(-time.Second)
	for i := 0; i < 6; i++ {
		createCheck(t, checkRepo, domain.Check{CheckID: fmt.Sprintf("check-%d", i), Enabled: true, NextCheckAt: &dueAt})
	}

	var current int32
	var maxSeen int32
	s := New(mgr.Repositories(), Options{
		InstanceID:     "monitor-a",
		MaxConcurrency: 2,
		Runner: runnerFunc(func(ctx context.Context, check domain.Check) domain.CheckResult {
			now := atomic.AddInt32(&current, 1)
			for {
				seen := atomic.LoadInt32(&maxSeen)
				if now <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, now) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&current, -1)
			return okResult(check)
		}),
	})
	if _, err := s.RunDueOnce(ctx); err != nil {
		t.Fatalf("RunDueOnce: %v", err)
	}
	if maxSeen > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", maxSeen)
	}
}

func TestSchedulerUpdatesWatchdogMetricsAfterPersist(t *testing.T) {
	ctx := context.Background()
	mgr := openSchedulerDB(t)
	dueAt := time.Now().Add(-time.Second)
	createCheck(t, mgr.Repositories().Checks, domain.Check{CheckID: "ready", Kind: domain.CheckKindHTTP, Enabled: true, NextCheckAt: &dueAt})
	registry := prometheus.NewRegistry()
	metrics, err := watchdog.NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	s := New(mgr.Repositories(), Options{
		Runner: runnerFunc(func(context.Context, domain.Check) domain.CheckResult {
			return domain.CheckResult{Success: true, LatencyMS: 25}
		}),
		Watchdog: metrics,
	})
	if _, err := s.RunDueOnce(ctx); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, family := range families {
		if family.GetName() == "moox_monitor_watchdog_checks_total" && len(family.GetMetric()) == 1 && family.GetMetric()[0].GetCounter().GetValue() == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("watchdog success counter was not observed")
	}
}

type runnerFunc func(context.Context, domain.Check) domain.CheckResult

func (f runnerFunc) Run(ctx context.Context, check domain.Check) domain.CheckResult {
	return f(ctx, check)
}

func okResult(check domain.Check) domain.CheckResult {
	return domain.CheckResult{
		SpaceID:   check.SpaceID,
		CheckID:   check.CheckID,
		Success:   true,
		Status:    domain.CheckStatusOK,
		CheckedAt: time.Now().UTC(),
	}
}

func createCheck(t *testing.T, repo *store.CheckRepository, check domain.Check) {
	t.Helper()
	if check.Name == "" {
		check.Name = check.CheckID
	}
	if check.Kind == "" {
		check.Kind = domain.CheckKindHTTP
	}
	if check.IntervalSeconds == 0 {
		check.IntervalSeconds = 60
	}
	if check.TimeoutMS == 0 {
		check.TimeoutMS = 1000
	}
	if check.Method == "" {
		check.Method = "GET"
	}
	if check.ExpectedStatus == "" {
		check.ExpectedStatus = "200-299"
	}
	if check.Headers == "" {
		check.Headers = "{}"
	}
	if check.Labels == "" {
		check.Labels = "{}"
	}
	if check.Source == "" {
		check.Source = domain.CheckSourceObservability
	}
	if err := repo.Create(context.Background(), &check); err != nil {
		t.Fatalf("create check %s: %v", check.CheckID, err)
	}
}

func openSchedulerDB(t *testing.T) *store.Store {
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
