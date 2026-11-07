package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type countingMaintainer struct{ runs atomic.Int32 }

func (m *countingMaintainer) RunAll(context.Context) error {
	m.runs.Add(1)
	return nil
}

// 启动时运行一次，唤醒（资金设置变更）时再运行一次。
func TestFundWorkerRunsAtStartAndOnWake(t *testing.T) {
	maintainer := &countingMaintainer{}
	worker := &FundWorker{Maintainer: maintainer, Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool { return maintainer.runs.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	worker.Wake("space-1", "logical-1")
	require.Eventually(t, func() bool { return maintainer.runs.Load() == 2 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// 生命周期门禁关闭（系统或手动暂停、恢复追赶、重置）时不运行。
func TestFundWorkerHonoursTheLifecycleGate(t *testing.T) {
	maintainer := &countingMaintainer{}
	var open atomic.Bool
	worker := &FundWorker{Maintainer: maintainer, Interval: time.Hour, Gate: open.Load}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	worker.Wake("", "")
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, maintainer.runs.Load(), "门禁关闭时启动和唤醒都不运行")
	open.Store(true)
	worker.Wake("", "")
	require.Eventually(t, func() bool { return maintainer.runs.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
}

func TestFundWorkerWithoutAMaintainerReportsConfigError(t *testing.T) {
	require.ErrorIs(t, (&FundWorker{}).Run(context.Background()), errFundWorkerConfig)
}
