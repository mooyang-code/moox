package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// strictResultStore 按比较交换迁移投递状态：from 与当前状态不符时返回 ErrNotFound。
type strictResultStore struct {
	row store.Result
}

func (s *strictResultStore) ListPendingResults(context.Context, int) ([]store.Result, error) {
	if s.row.PublishStatus != store.PublishPending {
		return nil, nil
	}
	return []store.Result{s.row}, nil
}

func (s *strictResultStore) PreparePendingResult(context.Context, string, time.Time) (store.Result, bool, error) {
	return s.row, true, nil
}

func (s *strictResultStore) TransitionPublishStatus(_ context.Context, _ string, from, to store.PublishStatus) error {
	if s.row.PublishStatus != from {
		return store.ErrNotFound
	}
	s.row.PublishStatus = to
	return nil
}

// cancellingPublisher 在发布期间模拟更新的结果把这一行取消，然后发布成功。
type cancellingPublisher struct {
	store *strictResultStore
}

func (p *cancellingPublisher) PublishResult(context.Context, store.Result) error {
	p.store.row.PublishStatus = store.PublishCancelled
	return nil
}

// 事件已经发出、这一行在发布期间被并发取消：仍记为 sent，如实反映投递状态。
func TestRelayMarksPublishedRowSentAfterConcurrentCancel(t *testing.T) {
	resultStore := &strictResultStore{row: store.Result{ResultID: "r1", PublishStatus: store.PublishPending}}
	relay := &Relay{Store: resultStore, Publisher: &cancellingPublisher{store: resultStore}}
	if err := relay.PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if resultStore.row.PublishStatus != store.PublishSent {
		t.Fatalf("已发出的结果应记为 sent：%s", resultStore.row.PublishStatus)
	}
}

// brokenClient 已连接，但发布一直得不到确认（例如 stream 不可用、没有发布权限）。
type brokenClient struct{}

func (brokenClient) Ready() bool                    { return true }
func (brokenClient) Close() error                   { return nil }
func (brokenClient) EventPublisher() EventPublisher { return brokenPublisher{} }

type brokenPublisher struct{}

func (brokenPublisher) PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	return nil, errors.New("nats: no response from stream")
}

// 发布持续失败时记录起点：短 bar 的实例新结果不断替代旧的待投递结果，最老一条的年龄不会超过一根 bar，
// 停滞只能看发布本身失败了多久；恢复后一轮正常完成即清零。
func TestRuntimeTracksPublishFailures(t *testing.T) {
	data, err := strategyEventData("fail-1")
	if err != nil {
		t.Fatal(err)
	}
	resultStore := &runtimeTestStore{row: store.Result{ResultID: "fail-1", EventData: data, PublishStatus: store.PublishPending, CreatedAt: time.Now()}}
	var healthy atomic.Bool
	runtime, err := NewRuntime(RuntimeConfig{
		Store: resultStore, InstanceID: "strategy-test", RelayInterval: 5 * time.Millisecond, ReconnectInterval: 5 * time.Millisecond, BatchSize: 1,
		Probe: func(context.Context, JetStreamClient) error { return nil },
		Connector: func(context.Context) (JetStreamClient, error) {
			if healthy.Load() {
				return newRuntimeTestClient(), nil
			}
			return brokenClient{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	eventually(t, time.Second, func() bool { return runtime.PublishFailingFor(time.Now()) > 0 })
	first := runtime.PublishFailingFor(time.Now())
	time.Sleep(30 * time.Millisecond)
	if again := runtime.PublishFailingFor(time.Now()); again <= first {
		t.Fatalf("持续失败时起点不应前移：%s → %s", first, again)
	}
	healthy.Store(true)
	eventually(t, 2*time.Second, func() bool { return resultStore.isPublished() && runtime.PublishFailingFor(time.Now()) == 0 })
}
