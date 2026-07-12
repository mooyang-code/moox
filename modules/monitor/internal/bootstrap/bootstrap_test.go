package bootstrap

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestMonitorHealthSnapshotReportsClosedDatabaseAsNotReady(t *testing.T) {
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	db := mgr.DB()
	if err := mgr.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	previous := defaultScheduler
	defaultScheduler = scheduler.New(db, scheduler.Options{})
	t.Cleanup(func() { defaultScheduler = previous })

	cfg := config.Default()
	cfg.Instance.InstanceID = "monitor-test"
	cfg.Metrics.Enabled = false
	rsp := monitorHealthSnapshot(cfg, mgr, nil)(context.Background())
	if rsp.Ready {
		t.Fatalf("health response = %+v, want not ready", rsp)
	}
}

func TestActiveMonitorInstanceIDsIncludesLocalAndActivePeers(t *testing.T) {
	ctx := context.Background()
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	repo := repository.NewPeerRepository(mgr.DB())
	now := time.Now()
	for _, instance := range []*domain.MonitorInstance{
		{InstanceID: "monitor-b", Status: domain.InstanceStatusActive, LastSeenAt: &now},
		{InstanceID: "monitor-c", Status: domain.InstanceStatusDown, LastSeenAt: &now},
		{InstanceID: "monitor-a", Status: domain.InstanceStatusActive, LastSeenAt: &now},
	} {
		if err := repo.UpsertInstance(ctx, instance); err != nil {
			t.Fatalf("upsert instance: %v", err)
		}
	}
	got := activeMonitorInstanceIDs(ctx, "monitor-a", repo, time.Hour)
	want := []string{"monitor-a", "monitor-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("active ids = %v, want %v", got, want)
	}
}

func TestActiveMonitorInstanceIDsSkipsStaleAndDisabledPeers(t *testing.T) {
	ctx := context.Background()
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	repo := repository.NewPeerRepository(mgr.DB())
	stale := time.Now().Add(-time.Hour)
	if err := repo.UpsertInstance(ctx, &domain.MonitorInstance{
		InstanceID: "monitor-stale",
		Status:     domain.InstanceStatusActive,
		LastSeenAt: &stale,
	}); err != nil {
		t.Fatalf("upsert stale instance: %v", err)
	}
	if got := activeMonitorInstanceIDs(ctx, "monitor-a", repo, 3*time.Second); !reflect.DeepEqual(got, []string{"monitor-a"}) {
		t.Fatalf("active ids with stale peer = %v", got)
	}
	if got := activeMonitorInstanceIDs(ctx, "monitor-a", repo, 0); !reflect.DeepEqual(got, []string{"monitor-a"}) {
		t.Fatalf("active ids with peers disabled = %v", got)
	}
}
