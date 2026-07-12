package control

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/health"
)

func TestCollectorHealthSnapshot(t *testing.T) {
	cfg := Default()
	dbm := NewManager()
	if err := dbm.Initialize(&DatabaseConfig{Path: filepath.Join(t.TempDir(), "collector.db")}); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	state := health.New("collector", "collector", "", "")
	rsp := collectorHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "collector" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["storage_metadata_target"] != "127.0.0.1:20100" {
		t.Fatalf("storage_metadata_target = %v", rsp.Details["storage_metadata_target"])
	}
}
