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
	// Name 是任务名，只用在出错日志里，默认“资金维护任务”。
	Name string

	once sync.Once
	wake chan struct{}
	// ready 记录已经因“会话就绪”唤醒过的执行账户：每个账户每个进程只唤醒一次，之后靠定时器和设置变更。
	ready sync.Map
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

// AccountReady 在执行账户的会话第一次就绪时唤醒一次：进程刚启动时账户还没就绪，启动那一轮什么都做不了，
// 不唤醒的话 BNB 抵扣和补买要等到下一个整点间隔。会话每次同步都会就绪，所以只在第一次唤醒。
func (w *FundWorker) AccountReady(tradingAccountID string) {
	if w == nil {
		return
	}
	if _, seen := w.ready.LoadOrStore(tradingAccountID, struct{}{}); !seen {
		w.Wake("", "")
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
		name := w.Name
		if name == "" {
			name = "资金维护任务"
		}
		log.Warnf("交易关注项：%s有失败，下次再试：%v", name, err)
	}
}
