package bootstrap

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/health"
)

func TestFactorHealthSnapshot(t *testing.T) {
	cfg := Default()
	state := health.New("factor", cfg.Instance.InstanceID, "", "")
	rsp := factorHealthSnapshot(cfg, nil, nil, nil, state)(context.Background())

	if rsp.Module != "factor" || rsp.Ready || rsp.Status != "degraded" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["worker_count"] != cfg.Engine.Workers {
		t.Fatalf("worker_count = %v, want %d", rsp.Details["worker_count"], cfg.Engine.Workers)
	}
}
