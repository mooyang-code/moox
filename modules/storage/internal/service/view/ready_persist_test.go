package view

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// openReadyFence 让测试中的第二个服务实例从同一目录载入就绪队列与写入围栏，模拟进程重启。
func (s *Service) openReadyFence(dir string) error {
	s.readyFenceDir = strings.TrimSpace(dir)
	if s.appliedFence == nil {
		s.appliedFence = make(map[appliedFenceKey]uint64)
	}
	return s.loadReadyFence()
}

// readyGatedPublisher 让第一条发布立即成功，之后的发布挂起直到 release 关闭。
type readyGatedPublisher struct {
	calls   atomic.Int32
	started chan string
	release chan struct{}
}

func (p *readyGatedPublisher) Publish(ctx context.Context, _ events.Event, _ proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	if p.calls.Add(1) == 1 {
		return &jetstream.PublishAck{Sequence: 1}, nil
	}
	p.started <- opts.EventID
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return &jetstream.PublishAck{Sequence: 2}, nil
	}
}

// 一次刷新排空多条事件时只在结束时落盘一次，而不是每移出一条就整体重写一次队列文件（写盘量 n²）：
// 刷新途中已发布的事件仍留在文件里，进程此时退出只会在重启后重复发布，由 Msg-Id 去重。
func TestViewDataReadyPersistsOncePerFlush(t *testing.T) {
	publisher := &readyGatedPublisher{started: make(chan string, 1), release: make(chan struct{})}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	service.readyFenceDir = t.TempDir()
	for minute, id := range []string{"prices-ready-1", "prices-ready-2"} {
		at := time.Date(2026, 9, 13, 16, minute, 0, 0, time.UTC)
		payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: uint64(12 + minute)}}
		_ = service.HandleCollectorPeriodCompleted(context.Background(), periodMessage(id, at), payload)
	}
	service.pendingReadyMu.Lock()
	first := service.pendingReady[0].opts.EventID
	service.pendingReadyMu.Unlock()
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 13)
	done := make(chan error, 1)
	go func() { done <- service.FlushViewDataReady(context.Background(), "quant", "source-view") }()
	<-publisher.started
	persisted := func() string {
		raw, err := os.ReadFile(filepath.Join(service.readyFenceDir, "pending.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if service.pendingReadyContains(first) {
		t.Fatal("第一条发布成功后应移出内存队列")
	}
	if !strings.Contains(persisted(), first) {
		t.Fatal("刷新途中不应为移出一条事件重写队列文件")
	}
	close(publisher.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if service.hasPendingReady() || strings.Contains(persisted(), first) {
		t.Fatalf("刷新结束时应落盘一次排空后的队列：%s", persisted())
	}
}
