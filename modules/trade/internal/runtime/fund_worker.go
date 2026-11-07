package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// FundMaintainer 是资金维护任务的执行入口。
type FundMaintainer interface {
	RunAll(ctx context.Context) error
}

// FundWorker 定时（默认每小时）和被唤醒时（资金设置变更）运行资金维护：BNB 抵扣、BNB 补买、小额兑 BNB、稳定币调平。
// 三类任务共用这一个定时器，各自按自己的间隔判断要不要动手。
type FundWorker struct {
	Maintainer FundMaintainer
	Interval   time.Duration
	// Gate 非空时是生命周期门禁：返回 false（系统或手动暂停、恢复追赶、重置）时不运行。
	Gate func() bool
	// RunTimeout 是一次运行的最长时间，默认 5 分钟。
	RunTimeout time.Duration

	once sync.Once
	wake chan struct{}
}

func (w *FundWorker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake 请求尽快运行一次；已经有待运行的请求时合并。
func (w *FundWorker) Wake(string, string) {
	if w == nil {
		return
	}
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

var errFundWorkerConfig = errors.New("trade runtime: FundWorker is not configured")

// Run 启动时先运行一次，之后每个间隔和每次唤醒各运行一次，直到 ctx 结束。
func (w *FundWorker) Run(ctx context.Context) error {
	if w == nil || w.Maintainer == nil {
		return errFundWorkerConfig
	}
	w.init()
	interval := w.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.runOnce(ctx)
		case <-w.wake:
			w.runOnce(ctx)
		}
	}
}

func (w *FundWorker) runOnce(ctx context.Context) {
	if w.Gate != nil && !w.Gate() {
		return
	}
	timeout := w.RunTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := w.Maintainer.RunAll(runCtx); err != nil && ctx.Err() == nil {
		log.Warnf("交易关注项：资金维护任务有失败，下次再试：%v", err)
	}
}
