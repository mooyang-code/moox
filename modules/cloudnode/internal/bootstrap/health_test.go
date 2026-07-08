package bootstrap

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
)

func TestCloudNodeHealthSnapshot(t *testing.T) {
	cfg := config.Default()
	rsp := cloudnodeHealthSnapshot(cfg)(context.Background())

	if rsp.Module != "cloudnode" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["queue_backend"] != "jetstream" {
		t.Fatalf("queue_backend = %v", rsp.Details["queue_backend"])
	}
}
