package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

func TestCollectorHealthSnapshot(t *testing.T) {
	cfg := Default()
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("collector", "collector", "", "")
	rsp := collectorHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "collector" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["storage_metadata_target"] != "ip://127.0.0.1:11003" {
		t.Fatalf("storage_metadata_target = %v", rsp.Details["storage_metadata_target"])
	}
}
