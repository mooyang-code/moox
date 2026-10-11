package view

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
)

func crashWindowView() *pb.View {
	return &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"}
}

// 周期事件先到、行后到：行事件 ACK 前，满足积压就绪事件的写入围栏必须已经落盘。
// 否则 ACK 后、后台落盘前崩溃，重启后事件缺少来源位置，要等同一来源的下一次写入才放行。
func TestAppliedFenceDurableBeforeRowsAckWhenReadyPending(t *testing.T) {
	dir := t.TempDir()
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), crashWindowView())
	service.readyFenceDir = dir
	enqueueReadyPeriods(t, service, "prices-ready-1") // 要求 node-a/store-a 的位置 12

	rows := &storageeventpb.DatasetRowsUpserted{DatasetId: "prices", SourceNodeId: "node-a", SourceStoreId: "store-a", SourceSequence: 12}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	if err := service.makeAppliedDurableForPending("quant", rows); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：丢弃实例，只从磁盘恢复。
	publisher := newReadyPublisherFake()
	reloaded := newPeriodTestService(newPeriodMetadataFake(), publisher, crashWindowView())
	if err := reloaded.openReadyFence(dir); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.byID) != 1 || reloaded.hasPendingReady() {
		t.Fatalf("崩溃重启后无需新行也应发布就绪事件：published=%d pending=%v", len(publisher.byID), reloaded.hasPendingReady())
	}
}

// 没有积压事件依赖这条位置时不同步落盘（每行事件一次 fsync 的代价不可接受）。
func TestAppliedFenceNotFlushedSynchronouslyWithoutDependentReady(t *testing.T) {
	dir := t.TempDir()
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), crashWindowView())
	service.readyFenceDir = dir
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 3)
	rows := &storageeventpb.DatasetRowsUpserted{DatasetId: "prices", SourceNodeId: "node-a", SourceStoreId: "store-a", SourceSequence: 3}
	if err := service.makeAppliedDurableForPending("quant", rows); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "applied.json")); err == nil {
		t.Fatal("没有依赖它的积压事件时不应同步落盘")
	}
}

// 行先到、周期事件后到：位置在内存里已满足，就绪事件入队落盘时必须先让围栏落盘。
func TestAppliedFenceFlushedBeforePendingReadyPersisted(t *testing.T) {
	dir := t.TempDir()
	service := newPeriodTestService(newPeriodMetadataFake(), &readyErrorPublisher{inner: newReadyPublisherFake(), cancel: func() {}}, crashWindowView())
	service.readyFenceDir = dir
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	at := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
	_ = service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-late", at), payload)
	if _, err := os.Stat(filepath.Join(dir, "applied.json")); err != nil {
		t.Fatalf("就绪事件落盘前围栏必须先落盘：%v", err)
	}
	reloaded := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), crashWindowView())
	if err := reloaded.openReadyFence(dir); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.appliedSequence("quant", "source-view", "source-view-a", "node-a", "store-a"); got != 12 {
		t.Fatalf("重启后围栏应为 12：%d", got)
	}
}
