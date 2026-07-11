package control

import (
	"context"
	"testing"
	"time"
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

func TestCollectorHealthReportsStandbyAsNotReady(t *testing.T) {
	leader := &Leader{now: time.Now}
	rsp := collectorHealthSnapshot(Default(), leader)(context.Background())
	if rsp.Ready || rsp.Details["control_leadership"] != "standby" {
		t.Fatalf("health=%+v", rsp)
	}
}
