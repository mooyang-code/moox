package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

// 多个分区消费者并行写行时，写入围栏的落盘必须串行：读者任何时刻都只能读到完整的文件，最终文件是最新的快照。
func TestAppliedFencePersistsSafelyUnderConcurrentWriters(t *testing.T) {
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	service.readyFenceDir = t.TempDir()
	path := filepath.Join(service.readyFenceDir, "applied.json")
	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readErr <- nil
				return
			default:
			}
			raw, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				readErr <- err
				return
			}
			var items []persistedApplied
			if err := json.Unmarshal(raw, &items); err != nil {
				readErr <- fmt.Errorf("读到不完整的文件（%d 字节）：%w", len(raw), err)
				return
			}
		}
	}()
	// 记录位置的分区消费者与落盘（后台循环、停止消费）并发进行。
	var wg sync.WaitGroup
	for writer := 0; writer < 32; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for seq := uint64(1); seq <= 20; seq++ {
				service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", fmt.Sprintf("store-%d", writer), seq)
				if seq%5 == 0 {
					if err := service.flushAppliedFence(); err != nil {
						t.Error(err)
					}
				}
			}
		}(writer)
	}
	wg.Wait()
	if err := service.flushAppliedFence(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	reloaded := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	if err := reloaded.openReadyFence(service.readyFenceDir); err != nil {
		t.Fatal(err)
	}
	for writer := 0; writer < 32; writer++ {
		if seq := reloaded.appliedSequence("quant", "source-view", "source-view-a", "node-a", fmt.Sprintf("store-%d", writer)); seq != 20 {
			t.Fatalf("store-%d 的围栏应为 20：%d", writer, seq)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(service.readyFenceDir, "*.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("不应留下临时文件：%v", matches)
	}
}

// 落盘文件损坏（例如旧版本并发写出的半截文件）不能让 View 角色起不来：改名保留后按空状态启动。
func TestOpenReadyFenceSetsAsideCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"applied.json", "pending.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`[{"space_id":"quant"}]{`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	if err := service.openReadyFence(dir); err != nil {
		t.Fatalf("文件损坏时应按空状态启动：%v", err)
	}
	for _, name := range []string{"applied.json", "pending.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s 应被改名保留：%v", name, err)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, name+".corrupt-*")); len(matches) != 1 {
			t.Fatalf("%s 应留下一份 .corrupt 备份：%v", name, matches)
		}
	}
	if service.hasPendingReady() {
		t.Fatal("损坏的就绪队列不应载入任何事件")
	}
}

// readyErrorPublisher 依次返回预设的错误，之后委托给 inner。
type readyErrorPublisher struct {
	inner  *readyPublisherFake
	errs   []error
	cancel context.CancelFunc
}

func (p *readyErrorPublisher) Publish(ctx context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
		return nil, fmt.Errorf("%w: %w", jetstream.ErrConnection, context.Canceled)
	}
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return nil, err
	}
	return p.inner.Publish(ctx, event, payload, opts)
}

func enqueueReadyPeriods(t *testing.T, service *Service, ids ...string) {
	t.Helper()
	for i, id := range ids {
		at := time.Date(2026, 9, 13, 16, i, 0, 0, time.UTC)
		payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
		if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage(id, at), payload); !errors.Is(err, ErrViewDataReadyPending) {
			t.Fatalf("%s 应等待行写入：%v", id, err)
		}
	}
}

// 调用方已结束（进程关闭、消费者重启）时发布失败不是连接故障：不退避、不重连，事件留在队列里。
func TestViewDataReadyCancelledFlushDoesNotBackOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := &readyErrorPublisher{inner: newReadyPublisherFake(), cancel: cancel}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	reconnects := 0
	service.readyReconnect = func(context.Context) error {
		reconnects++
		return nil
	}
	enqueueReadyPeriods(t, service, "prices-ready-1")
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	if err := service.FlushViewDataReady(ctx, "quant", "source-view"); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消：%v", err)
	}
	if service.inReadyBackoff() || reconnects != 0 || !service.hasPendingReady() {
		t.Fatalf("取消不应进入退避或重连，事件应保留：backoff=%v reconnects=%d", service.inReadyBackoff(), reconnects)
	}
}

// 确定性的发布错误（超过 EventBus 最大消息长度、无法通过发布前校验）隔离该条，不拖住其后的事件。
func TestViewDataReadyQuarantinesPermanentPublishErrors(t *testing.T) {
	inner := newReadyPublisherFake()
	publisher := &readyErrorPublisher{inner: inner, errs: []error{fmt.Errorf("%w: publish: %w", jetstream.ErrConnection, nats.ErrMaxPayload)}}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	enqueueReadyPeriods(t, service, "prices-ready-1", "prices-ready-2")
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatalf("永久错误应隔离而不是返回错误：%v", err)
	}
	if service.inReadyBackoff() || service.hasPendingReady() || len(inner.byID) != 1 {
		t.Fatalf("应隔离第一条、发布第二条：backoff=%v pending=%v published=%d", service.inReadyBackoff(), service.hasPendingReady(), len(inner.byID))
	}
}

// 就绪队列落盘失败时周期事件不能被 ACK：返回错误由重投再次入队。
func TestEnqueueReadyPersistFailureIsNotAcked(t *testing.T) {
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.readyFenceDir = blocked
	at := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
	err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-1", at), payload)
	if err == nil || errors.Is(err, ErrViewDataReadyPending) || !strings.Contains(err.Error(), "落盘失败") {
		t.Fatalf("落盘失败应返回错误、不 ACK：%v", err)
	}
}

// 所属 View 已不在目录中的就绪事件超过保留时长后隔离，不永久占着队列；队列深度与最老事件随之上报。
func TestViewDataReadyDropsOrphanedEvents(t *testing.T) {
	service := newPeriodTestService(newPeriodMetadataFake(), newReadyPublisherFake(), &pb.View{SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a"})
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	service.SetMetrics(metrics)
	old := pendingViewReady{spaceID: "quant", viewID: "deleted-view", payload: &storageeventpb.ViewDataReady{ViewId: "deleted-view"}, opts: events.PublishOptions{EventID: "orphan-old", OccurredAt: time.Now().Add(-25 * time.Hour)}}
	fresh := pendingViewReady{spaceID: "quant", viewID: "deleted-view", payload: &storageeventpb.ViewDataReady{ViewId: "deleted-view"}, opts: events.PublishOptions{EventID: "orphan-fresh", OccurredAt: time.Now()}}
	service.pendingReadyMu.Lock()
	service.pendingReady = []pendingViewReady{old, fresh}
	service.observeReadyQueueLocked()
	service.pendingReadyMu.Unlock()
	if snapshot := service.metrics.Snapshot(); snapshot.ReadyQueuePending != 2 || snapshot.ReadyQueueOldestAge < 24*time.Hour {
		t.Fatalf("应上报队列深度与最老事件：%+v", snapshot)
	}
	if err := service.FlushViewDataReady(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if service.pendingReadyContains("orphan-old") || !service.pendingReadyContains("orphan-fresh") {
		t.Fatal("只应隔离超过保留时长的孤儿事件")
	}
	if snapshot := service.metrics.Snapshot(); snapshot.ReadyQueuePending != 1 {
		t.Fatalf("隔离后应更新队列深度：%+v", snapshot)
	}
}
