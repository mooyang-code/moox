package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/health"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
)

func TestCloudNodeHealthSnapshot(t *testing.T) {
	cfg := config.Default()
	dbm := storage.NewManager()
	if err := dbm.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")}); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("cloudnode", "cloudnode", "", "")
	rsp := cloudnodeHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "cloudnode" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["queue_backend"] != "jetstream" {
		t.Fatalf("queue_backend = %v", rsp.Details["queue_backend"])
	}
}
