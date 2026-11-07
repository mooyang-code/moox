package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// slowSender 对地址里带 slow 的规则一直阻塞到 release，其余立即成功。
type slowSender struct {
	release chan struct{}
	started atomic.Int32
	mu      sync.Mutex
	done    []string
}

func (s *slowSender) Send(_ context.Context, target delivery.Target, _ delivery.Request) delivery.Result {
	if target.URL == "https://slow.example.com/hook" {
		s.started.Add(1)
		<-s.release
	}
	s.mu.Lock()
	s.done = append(s.done, target.URL)
	s.mu.Unlock()
	return delivery.Result{Class: delivery.Success, StatusCode: 200}
}

func (s *slowSender) finished() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.done...)
}

func seedMany(t *testing.T, s *store.Store, c *clock, ruleID, url string, count int) {
	t.Helper()
	ctx := context.Background()
	rule := notifyRule(ruleID)
	rule.NotifyURL = url
	require.NoError(t, s.CreateRule(ctx, rule))
	require.NoError(t, s.InTx(ctx, func(tx *store.Tx) error {
		for i := 0; i < count; i++ {
			e := store.Execution{
				ExecutionID: ruleID + "-" + string(rune('a'+i)), RuleID: ruleID, RuleName: "规则 " + ruleID, ActionType: store.ActionNotifyURL,
				EventID: ruleID + "-event-" + string(rune('a'+i)), EventName: delistEvent, SpaceID: "mooxsys", SubjectID: "binance:SPOT:FOOUSDT",
				OccurredAt: c.now.UnixMilli(), PayloadJSON: `{}`, Status: store.StatusPending, NextRunAt: c.now.UnixMilli(), CreatedAt: c.now.UnixMilli(), UpdatedAt: c.now.UnixMilli(),
			}
			if err := tx.InsertExecution(e); err != nil {
				return err
			}
		}
		return nil
	}))
}

// 一条规则的慢接口不拖住别的规则：Run 领取后不等处理完，每条规则最多同时处理 MaxPerRule 条，剩下的名额留给别的规则。
func TestSlowRuleDoesNotStarveOtherRules(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := openStore(t)
	c := &clock{now: baseTime}
	seedMany(t, s, c, "slow", "https://slow.example.com/hook", 6)
	seedMany(t, s, c, "fast", "https://fast.example.com/hook", 3)
	sender := &slowSender{release: make(chan struct{})}
	worker := newWorker(t, s, sender, nil, c)
	worker.Batch = 4
	worker.PollInterval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool {
		fast := 0
		for _, url := range sender.finished() {
			if url == "https://fast.example.com/hook" {
				fast++
			}
		}
		return fast == 3
	}, 3*time.Second, 10*time.Millisecond, "慢规则卡住时，别的规则照常处理")
	require.Eventually(t, func() bool { return sender.started.Load() == MaxPerRule }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, MaxPerRule, sender.started.Load(), "慢规则同时最多处理 MaxPerRule 条，其余的排队")

	close(sender.release)
	require.Eventually(t, func() bool { return len(sender.finished()) == 9 }, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// 处理它的线程卡死或丢了结果：停在“执行中”太久的记录被放回待处理，没超时的不动。
func TestStaleRunningExecutionsAreRecovered(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seedMany(t, s, c, "r1", "https://example.com/hook", 2)
	claimed, err := s.ClaimDue(ctx, c.now.UnixMilli(), 10, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 2)

	recovered, err := s.RecoverStaleRunning(ctx, c.now.Add(-StaleRunningAfter).UnixMilli(), c.now.UnixMilli())
	require.NoError(t, err)
	require.Zero(t, recovered, "刚领取的不算卡住")

	later := c.now.Add(StaleRunningAfter + time.Minute)
	recovered, err = s.RecoverStaleRunning(ctx, later.Add(-StaleRunningAfter).UnixMilli(), later.UnixMilli())
	require.NoError(t, err)
	require.EqualValues(t, 2, recovered)
	got, err := s.GetExecution(ctx, "r1-a")
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, got.Status)
	require.Equal(t, later.UnixMilli(), got.NextRunAt)

	// 放回之后正常处理。
	c.now = later
	worker := newWorker(t, s, &fakeSender{results: []delivery.Result{{Class: delivery.Success, StatusCode: 200}}}, action.NewRegistry(), c)
	require.Equal(t, 2, worker.RunOnce(ctx))
}
