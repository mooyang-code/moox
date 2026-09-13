package view

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestViewDataReadyFenceWaitsUntilRowsApplied(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	at := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	message := periodMessage("prices-ready", at)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, 1786032000)
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{
		{NodeId: "node-a", StoreId: "store-a", Sequence: 7},
	}
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, payload); err != nil {
		t.Fatalf("unapplied completion should be retained, not fail hard: %v", err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatal("ViewDataReady published before committed rows were applied")
	}

	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 7)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.byID) != 1 {
		t.Fatalf("ready after apply unique=%d", len(publisher.byID))
	}
}

func TestViewDataReadyFenceRequiresEveryPartition(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	at := time.Date(2026, 9, 13, 16, 1, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT", "ETH-USDT"}, nil, at, 1786032060)
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{
		{NodeId: "node-a", StoreId: "store-a", Sequence: 4},
		{NodeId: "node-b", StoreId: "store-b", Sequence: 9},
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 4)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-2", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatal("ViewDataReady published with one partition still unapplied")
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-b", "store-b", 9)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.byID) != 1 {
		t.Fatalf("ready after both partitions unique=%d", len(publisher.byID))
	}
}

func TestViewDataReadyFenceFilteredViewPublishesOwnScope(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher,
		&pb.View{SpaceId: "quant", ViewId: "spot-view", DatasetId: "prices", ActiveIndexId: "spot-a", FilterJson: `{"market":"spot"}`},
		&pb.View{SpaceId: "quant", ViewId: "swap-view", DatasetId: "prices", ActiveIndexId: "swap-a", FilterJson: `{"market":"swap"}`},
	)
	service.NoteAppliedPosition("quant", "spot-view", "spot-a", "node-a", "store-a", 1)
	service.NoteAppliedPosition("quant", "swap-view", "swap-a", "node-a", "store-a", 1)
	at := time.Date(2026, 9, 13, 16, 2, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, 1786032120)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-3", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.byID) != 2 {
		t.Fatalf("filtered views unique ready=%d", len(publisher.byID))
	}
	scopes := map[string]string{}
	for _, item := range publisher.attempts {
		ready := item.payload.(*storageeventpb.ViewDataReady)
		scopes[ready.GetViewId()] = ready.GetVisibleScope()
	}
	if scopes["spot-view"] == scopes["swap-view"] || scopes["spot-view"] == payload.GetExpectedScopeRef() {
		t.Fatalf("filtered views must publish their own visible_scope: %v", scopes)
	}
}

func TestViewDataReadyFenceRebuildDoesNotFalsePublish(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	view := &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"}
	service := newPeriodTestService(metadata, publisher, view)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 3)
	at := time.Date(2026, 9, 13, 16, 3, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, 1786032180)
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 3}}
	runtime := service.views[viewRef{spaceID: "quant", viewID: "source-view"}]
	runtime.mu.Lock()
	runtime.active = "source-view-b"
	runtime.mu.Unlock()
	service.catalogViews[viewRef{spaceID: "quant", viewID: "source-view"}].ActiveIndexId = "source-view-b"
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-4", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatal("rebuild switched index generation must not inherit old applied positions")
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-b", "node-a", "store-a", 3)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.byID) != 1 {
		t.Fatalf("ready after new generation unique=%d", len(publisher.byID))
	}
}

func TestViewDataReadyFencePublishRetryIsIdempotent(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 1)
	at := time.Date(2026, 9, 13, 16, 4, 0, 0, time.UTC)
	message := periodMessage("prices-ready-5", at)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, 1786032240)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, proto.Clone(payload).(*storageeventpb.CollectorPeriodCompleted)); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, proto.Clone(payload).(*storageeventpb.CollectorPeriodCompleted)); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 2 || len(publisher.byID) != 1 {
		t.Fatalf("idempotent retry attempts=%d unique=%d", len(publisher.attempts), len(publisher.byID))
	}
}
