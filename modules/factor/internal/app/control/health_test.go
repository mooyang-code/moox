package control

import (
	"context"
	"testing"
)

func TestFactorHealthSnapshot(t *testing.T) {
	cfg := Default()
	rsp := factorHealthSnapshot(cfg)(context.Background())

	if rsp.Module != "factor" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["worker_count"] != cfg.Engine.Workers {
		t.Fatalf("worker_count = %v, want %d", rsp.Details["worker_count"], cfg.Engine.Workers)
	}
}
