package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type fundingSettlerAdapter struct {
	execution.ExecutionAdapter
	calls atomic.Int32
}

func (a *fundingSettlerAdapter) SettleFunding(context.Context) (int, error) {
	a.calls.Add(1)
	return 0, errors.New("交易所暂时不可用")
}

// 会话一启动就先结算一次（补算停机期间的结算点）；出错之后照常继续检查，取消后协程结束。
func TestPaperFundingRunsAtStartAndSurvivesErrors(t *testing.T) {
	previous := paperFundingInterval
	paperFundingInterval = 10 * time.Millisecond
	t.Cleanup(func() { paperFundingInterval = previous })
	adapter := &fundingSettlerAdapter{}
	session := &ExchangeSession{Account: store.TradingAccountRecord{TradingAccountID: "paper-1"}, Adapter: adapter}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		session.runPaperFunding(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for adapter.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if adapter.calls.Load() < 3 {
		t.Fatalf("每次都出错，仍要继续检查：calls = %d", adapter.calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后资金费协程没有退出")
	}
}
