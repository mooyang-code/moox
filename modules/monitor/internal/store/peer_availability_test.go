package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
)

func TestMarkStaleWithAlertWaitsForConcurrentFreshObservation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	writer := openStoreAt(t, path, true)
	staleScanner := openStoreAt(t, path, false)
	old := time.Now().UTC().Add(-time.Minute)
	fresh := time.Now().UTC()
	if err := writer.Repositories().Peers.UpsertInstance(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &old, Snapshot: "{}"}); err != nil {
		t.Fatal(err)
	}

	var tx *gorm.DB
	if err, openErr := WithDatabase(writer, func(db *gorm.DB) error {
		tx = db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return tx.Error
		}
		return tx.Model(&domain.MonitorInstance{}).Where("c_instance_id = ?", "monitor-b").Update("c_last_seen_at", fresh).Error
	}); openErr != nil || err != nil {
		t.Fatalf("hold fresh observation: callback=%v db=%v", err, openErr)
	}

	type result struct {
		ids []string
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := staleScanner.Repositories().Peers.MarkStaleWithAlert(ctx, fresh.Add(-time.Second), PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: fresh})
		done <- result{ids: ids, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("stale scan completed before the concurrent observation committed: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || len(got.ids) != 0 {
		t.Fatalf("stale transitions = %v, err=%v", got.ids, got.err)
	}
	instance, err := staleScanner.Repositories().Peers.GetInstance(ctx, "monitor-b")
	if err != nil || instance.Status != domain.InstanceStatusActive || instance.LastSeenAt == nil || !instance.LastSeenAt.Equal(fresh) {
		t.Fatalf("instance after interleaving = %+v, err=%v", instance, err)
	}
}

func TestPeerAvailabilityTransitionRollsBackAndRetriesAfterAlertFailure(t *testing.T) {
	ctx := context.Background()
	mgr := openTestDB(t)
	repos := mgr.Repositories()
	old := time.Now().UTC().Add(-time.Minute)
	now := time.Now().UTC()
	if err := repos.Peers.UpsertInstance(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &old, Snapshot: "{}"}); err != nil {
		t.Fatal(err)
	}
	createAlertAbortTrigger(t, mgr, domain.AlertEventTriggered)
	if _, err := repos.Peers.MarkStaleWithAlert(ctx, now.Add(-time.Second), PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: now}); err == nil {
		t.Fatal("MarkStaleWithAlert error = nil, want injected alert failure")
	}
	assertPeerAlertStatus(t, repos, "monitor-b", domain.InstanceStatusActive, "", 0)
	dropAlertAbortTrigger(t, mgr)
	ids, err := repos.Peers.MarkStaleWithAlert(ctx, now.Add(-time.Second), PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: now})
	if err != nil || len(ids) != 1 || ids[0] != "monitor-b" {
		t.Fatalf("retry stale transitions = %v, err=%v", ids, err)
	}
	assertPeerAlertStatus(t, repos, "monitor-b", domain.InstanceStatusDown, domain.AlertStatusFiring, 1)

	createAlertAbortTrigger(t, mgr, domain.AlertEventResolved)
	recoveredAt := now.Add(time.Second)
	transitioned, err := repos.Peers.UpsertActiveWithAlert(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &recoveredAt, Snapshot: "{\"ok\":true}"}, PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: recoveredAt})
	if err == nil || transitioned {
		t.Fatalf("recovery with injected failure transitioned=%v err=%v", transitioned, err)
	}
	assertPeerAlertStatus(t, repos, "monitor-b", domain.InstanceStatusDown, domain.AlertStatusFiring, 1)
	dropAlertAbortTrigger(t, mgr)
	transitioned, err = repos.Peers.UpsertActiveWithAlert(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &recoveredAt, Snapshot: "{\"ok\":true}"}, PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: recoveredAt})
	if err != nil || !transitioned {
		t.Fatalf("retry recovery transitioned=%v err=%v", transitioned, err)
	}
	assertPeerAlertStatus(t, repos, "monitor-b", domain.InstanceStatusActive, domain.AlertStatusResolved, 2)
	state, err := repos.Alerts.GetState(ctx, peerAlertSpaceID, peerAlertCheckID("monitor-b"), peerAlertCheckID("monitor-b"))
	if err != nil || state.TriggeredAt == nil || !state.TriggeredAt.Equal(now) || state.ResolvedAt == nil || !state.ResolvedAt.Equal(recoveredAt) {
		t.Fatalf("resolved state = %+v, err=%v", state, err)
	}
}

func TestConcurrentPeerRecoveryEmitsOneResolvedTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	first := openStoreAt(t, path, true)
	second := openStoreAt(t, path, false)
	now := time.Now().UTC()
	old := now.Add(-time.Minute)
	repos := first.Repositories()
	if err := repos.Peers.UpsertInstance(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &old, Snapshot: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Peers.MarkStaleWithAlert(ctx, now.Add(-time.Second), PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, peerRepo := range []*PeerRepository{first.Repositories().Peers, second.Repositories().Peers} {
		wg.Add(1)
		go func(repo *PeerRepository) {
			defer wg.Done()
			<-start
			seen := now.Add(time.Second)
			transitioned, err := repo.UpsertActiveWithAlert(ctx, &domain.MonitorInstance{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &seen, Snapshot: "{}"}, PeerTransitionOptions{Alerts: true, OwnerInstanceID: "monitor-a", OccurredAt: seen})
			results <- transitioned
			errs <- err
		}(peerRepo)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	transitionCount := 0
	for transitioned := range results {
		if transitioned {
			transitionCount++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if transitionCount != 1 {
		t.Fatalf("recovery transition count = %d", transitionCount)
	}
	assertPeerAlertStatus(t, first.Repositories(), "monitor-b", domain.InstanceStatusActive, domain.AlertStatusResolved, 2)
}

func openStoreAt(t *testing.T, path string, applySchema bool) *Store {
	t.Helper()
	mgr, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if applySchema {
		if err := mgr.ApplySchema(schema.SQL()); err != nil {
			t.Fatal(err)
		}
	}
	return mgr
}

func createAlertAbortTrigger(t *testing.T, mgr *Store, eventType string) {
	t.Helper()
	if eventType != domain.AlertEventTriggered && eventType != domain.AlertEventResolved {
		t.Fatalf("unsupported event type %q", eventType)
	}
	callbackErr, err := WithDatabase(mgr, func(db *gorm.DB) error {
		return db.Exec(fmt.Sprintf("CREATE TRIGGER abort_peer_alert BEFORE INSERT ON t_monitor_alert_events WHEN NEW.c_event_type = '%s' BEGIN SELECT RAISE(ABORT, 'injected alert failure'); END", eventType)).Error
	})
	if err != nil || callbackErr != nil {
		t.Fatalf("create abort trigger: callback=%v db=%v", callbackErr, err)
	}
}

func dropAlertAbortTrigger(t *testing.T, mgr *Store) {
	t.Helper()
	callbackErr, err := WithDatabase(mgr, func(db *gorm.DB) error { return db.Exec("DROP TRIGGER abort_peer_alert").Error })
	if err != nil || callbackErr != nil {
		t.Fatalf("drop abort trigger: callback=%v db=%v", callbackErr, err)
	}
}

func assertPeerAlertStatus(t *testing.T, repos *Repositories, instanceID, instanceStatus, alertStatus string, eventCount int) {
	t.Helper()
	ctx := context.Background()
	instance, err := repos.Peers.GetInstance(ctx, instanceID)
	if err != nil || instance.Status != instanceStatus {
		t.Fatalf("instance = %+v, err=%v", instance, err)
	}
	checkID := peerAlertCheckID(instanceID)
	state, stateErr := repos.Alerts.GetState(ctx, peerAlertSpaceID, checkID, checkID)
	if alertStatus == "" {
		if stateErr != gorm.ErrRecordNotFound {
			t.Fatalf("unexpected alert state = %+v, err=%v", state, stateErr)
		}
	} else if stateErr != nil || state.Status != alertStatus {
		t.Fatalf("alert state = %+v, err=%v", state, stateErr)
	}
	events, err := repos.Alerts.ListRecentEvents(ctx, 10)
	if err != nil || len(events) != eventCount {
		t.Fatalf("events = %+v, err=%v", events, err)
	}
}
