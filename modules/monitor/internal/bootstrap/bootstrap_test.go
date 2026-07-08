package bootstrap

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

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
	got := activeMonitorInstanceIDs(ctx, "monitor-a", repo)
	want := []string{"monitor-a", "monitor-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("active ids = %v, want %v", got, want)
	}
}
