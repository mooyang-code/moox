package repository

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildWorkItemUsesServerDefaultMaxAttempts(t *testing.T) {
	payload, err := structpb.NewStruct(map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := buildWorkItem(&pb.CloudWorkItem{
		SpaceId:      "space-a",
		OwnerService: "collector",
		OwnerRef:     "task-1",
		WorkloadType: "collector.binance.spot.kline",
		DeploymentId: "dep-1",
		Payload:      payload,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d", item.MaxAttempts, defaultMaxAttempts)
	}
	if item.DeploymentID != "dep-1" {
		t.Fatalf("deployment_id = %q", item.DeploymentID)
	}
}

func TestBuildWorkItemDefaultLeaseTimeout(t *testing.T) {
	item, err := buildWorkItem(&pb.CloudWorkItem{SpaceId: "space-a", OwnerService: "collector", OwnerRef: "x"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.LeaseTimeoutMs != defaultLeaseMillis {
		t.Fatalf("LeaseTimeoutMs = %d, want %d", item.LeaseTimeoutMs, defaultLeaseMillis)
	}
}

func TestBuildWorkItemRequiresSpaceID(t *testing.T) {
	_, err := buildWorkItem(&pb.CloudWorkItem{OwnerService: "collector", OwnerRef: "x"}, time.Now())
	if err == nil {
		t.Fatal("expected error for missing space_id")
	}
}

func TestWorkItemStatusToDBRejectsUnspecified(t *testing.T) {
	if _, ok := workItemStatusToDB(pb.WorkItemStatus_WORK_ITEM_STATUS_UNSPECIFIED); ok {
		t.Fatal("expected unspecified status to be rejected")
	}
}
