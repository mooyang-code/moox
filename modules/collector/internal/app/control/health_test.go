package control

import (
	"context"
	"testing"
)

func TestCollectorHealthSnapshot(t *testing.T) {
	cfg := Default()
	rsp := collectorHealthSnapshot(cfg)(context.Background())

	if rsp.Module != "collector" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["storage_metadata_target"] != "127.0.0.1:20100" {
		t.Fatalf("storage_metadata_target = %v", rsp.Details["storage_metadata_target"])
	}
}
