package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// countingPublisher 统计发布次数；err 非空时每次发布都失败。
type countingPublisher struct {
	calls int
	err   error
}

func (p *countingPublisher) PublishResult(context.Context, store.Result) error {
	p.calls++
	return p.err
}

// prepareFailingStore 让指定结果的准备步骤返回存储错误。
type prepareFailingStore struct {
	*recordingResultStore
	failID string
}

func (s *prepareFailingStore) PreparePendingResult(ctx context.Context, resultID string, at time.Time) (store.Result, bool, error) {
	if resultID == s.failID {
		return store.Result{}, false, errors.New("database is locked")
	}
	return s.recordingResultStore.PreparePendingResult(ctx, resultID, at)
}

func pendingRows(n int) []store.Result {
	rows := make([]store.Result, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, store.Result{ResultID: fmt.Sprintf("r%d", i), PublishStatus: store.PublishPending})
	}
	return rows
}

// 连接或 broker 问题：遇到第一个发布失败立即返回，不让同一批其余的结果逐条等满超时。
func TestRelayReturnsOnFirstPublishFailure(t *testing.T) {
	publisher := &countingPublisher{err: errors.New("nats: timeout")}
	relay := &Relay{Store: &recordingResultStore{rows: pendingRows(10)}, Publisher: publisher}
	err := relay.PublishPending(context.Background(), 100)
	var failure *PublishFailure
	if !errors.As(err, &failure) || publisher.calls != 1 {
		t.Fatalf("应在第一个发布失败时返回 PublishFailure：err=%v calls=%d", err, publisher.calls)
	}
}

// 同一批里先出现存储错误、后出现发布失败：返回发布失败，运行时才会断开重连。
func TestRelayPrefersPublishFailureOverStoreError(t *testing.T) {
	publisher := &countingPublisher{err: errors.New("nats: connection closed")}
	resultStore := &prepareFailingStore{recordingResultStore: &recordingResultStore{rows: pendingRows(2)}, failID: "r0"}
	err := (&Relay{Store: resultStore, Publisher: publisher}).PublishPending(context.Background(), 10)
	var failure *PublishFailure
	if !errors.As(err, &failure) {
		t.Fatalf("应返回发布失败：%v", err)
	}
}

// 连接、探针与投递的异常写日志：同一条错误连续出现只记一次，恢复后再记一条。
func TestRuntimeLogsIssuesOnceAndRecovery(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	var attempts atomic.Int32
	runtime, err := NewRuntime(RuntimeConfig{
		Store: &runtimeTestStore{}, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Probe: func(context.Context, JetStreamClient) error { return nil },
		Connector: func(context.Context) (JetStreamClient, error) {
			if attempts.Add(1) <= 3 {
				return nil, errors.New("broker unavailable")
			}
			return newRuntimeTestClient(), nil
		},
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	eventually(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(logs) >= 2
	})
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 || !strings.Contains(logs[0], "连接 EventBus 失败") || !strings.Contains(logs[1], "已恢复") {
		t.Fatalf("应只记一次连接失败和一次恢复：%q", logs)
	}
}
