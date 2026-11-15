package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"trpc.group/trpc-go/trpc-go/log"
)

// 策略模块的生命周期（设计 3.8）：模块级只有运行、暂停、恢复追赶三种状态，启用的实例都求值。
// 实盘还是影子由实例的确认列决定（store.Instance.Trial）：挂了账户、还没确认实盘的实例发影子目标（Trade 只换算记录，不下单），
// 用户在管理台或 moox-cli strategy instance confirm 确认后发正式目标。暂停：不求值、不发新目标，持仓不动。
// 模块自身的就绪检查（数据库、出站 EventBus、就绪事件消费者）不在生命周期里，由 moduleCheck 负责。

const (
	lifecycleRefreshEvery = 30 * time.Second
	// lifecycleStartupRefreshEvery、lifecycleStartupWindow：启动后模块自检还没通过时每秒检查一次（最多这么久），
	// 出站 EventBus 一连上就放行，不用等满一个常规检查周期；通过后或超过窗口回到常规周期。
	lifecycleStartupRefreshEvery = time.Second
	lifecycleStartupWindow       = time.Minute
	lifecycleStateFileName       = "lifecycle.json"
	lifecycleHealthDetailID      = "lifecycle"
)

// moduleCheck 是策略模块自身的就绪检查：数据库 Ping、出站 EventBus 已连、就绪事件消费者已连。周期刷新一个结果，
// 不通过时触发器返回 ModeWait 让事件稍后重投、参数提交暂停；启动时和运行中任一项失败都一样。初值是未通过：
// 第一次检查之前到达的事件也稍后重投。
type moduleCheck struct {
	run func(ctx context.Context) string // 返回第一项不通过的原因，全部通过时为空

	mu     sync.Mutex
	ok     bool
	reason string
}

func newModuleCheck(db *store.Store, eventConnected, consumerReady func() bool) *moduleCheck {
	return &moduleCheck{reason: "还没有做第一次检查", run: func(ctx context.Context) string {
		switch {
		case db == nil || db.Ping(ctx) != nil:
			return "数据库不可用"
		case eventConnected != nil && !eventConnected():
			return "出站 EventBus 未连接"
		case consumerReady != nil && !consumerReady():
			return "就绪事件消费者未连接"
		}
		return ""
	}}
}

// Refresh 重新检查一次。
func (m *moduleCheck) Refresh(ctx context.Context) {
	reason := m.run(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ok != (reason == "") {
		log.Infof("strategy_module_check_changed ready=%t reason=%s", reason == "", reason)
	}
	m.ok, m.reason = reason == "", reason
}

// Ready 报告最近一次检查是否通过；为 nil（测试）时视为通过。
func (m *moduleCheck) Ready() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ok
}

// Reason 返回最近一次检查不通过的原因。
func (m *moduleCheck) Reason() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

// runModuleCheck 启动后先快速检查（见 refreshUntilReady），之后按常规周期刷新，直到 ctx 结束。
func runModuleCheck(ctx context.Context, check *moduleCheck) {
	check.Refresh(ctx)
	refreshUntilReady(ctx, check, lifecycleStartupRefreshEvery, lifecycleStartupWindow)
	ticker := time.NewTicker(lifecycleRefreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check.Refresh(ctx)
		}
	}
}

// refreshUntilReady 在模块自检还没通过时按 every 反复检查，直到通过或超过 window。
// 重启时出站 EventBus 往往晚于第一次检查才连上，按常规 30 秒周期要等满一轮才放行，期间到达的事件只能等重投。
func refreshUntilReady(ctx context.Context, check *moduleCheck, every, window time.Duration) {
	deadline := time.Now().Add(window)
	for !check.Ready() && time.Now().Before(deadline) {
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		check.Refresh(ctx)
	}
}

// lifecycleDeps 是生命周期钩子依赖的模块状态。
type lifecycleDeps struct {
	db *store.Store
	// handler 在生命周期之后才创建（它的门禁依赖控制器），创建后绑定；追赶进度从它读取。
	handler *atomic.Pointer[trigger.Handler]
	// warm 是策略池的预热状态；为空表示没有预热（测试）。
	warm warmState
	// ready 是模块自检；放行前要通过。为空表示不检查（测试）。
	ready *moduleCheck
}

// warmState 是策略池预热状态的只读视图（*warmup.Manager 实现）。
type warmState interface {
	State(instanceID string) (warmup.Status, string)
	// Blocked 返回实例预热没完成或失败的说明；预热完成或不是池实例时为空串。
	Blocked(instanceID string) string
}

// catchupGrace 是恢复追赶目标之前留的宽限：最新一根刚收盘时因子结果还在路上，不把它算进追赶目标。
const catchupGrace = time.Minute

func newLifecycle(dbPath string, deps lifecycleDeps) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     logsink.Audit,
		Module:    "strategy",
		StateFile: filepath.Join(filepath.Dir(dbPath), lifecycleStateFileName),
		Hooks: lifecycle.Hooks{
			Catchup: deps.catchup,
			Quiesce: deps.quiesce,
			OnChange: func(from, to lifecycle.Stage, reason string) {
				log.Infof("strategy_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

// quiesceStep 是等待 outbox 排空时的轮询间隔。
const quiesceStep = 200 * time.Millisecond

// quiesce 等策略真正停下：正在求值的那一期结束，并且已经落库的目标都发布到了 EventBus（outbox 排空）。
// 门禁关闭之后不会再有新的目标；这里等的是门禁关闭之前已经产生、还留在 outbox 里的那些。
// 目标没有发到 EventBus 就停下，下游（Trade）暂停后它们再也送不出去，所以超时必须报出来，由系统暂停编排器拦住后续的暂停。
func (d lifecycleDeps) quiesce(ctx context.Context) error {
	if d.handler != nil {
		if handler := d.handler.Load(); handler != nil {
			if err := handler.WaitIdle(ctx); err != nil {
				return err
			}
		}
	}
	if d.db == nil {
		return nil
	}
	return waitOutboxDrained(ctx, d.db)
}

// outboxStatsReader 是等待 outbox 排空需要的最小能力（*store.Store 实现），便于测试替换。
type outboxStatsReader interface {
	PendingOutboxStats(ctx context.Context) (store.OutboxStats, error)
}

func waitOutboxDrained(ctx context.Context, reader outboxStatsReader) error {
	for {
		stats, err := reader.PendingOutboxStats(ctx)
		if err != nil {
			return err
		}
		if stats.PendingCount == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("还有 %d 个目标没有发布到 EventBus：%w", stats.PendingCount, ctx.Err())
		case <-time.After(quiesceStep):
		}
	}
}

// catchup 报告恢复追赶进度：模块自检通过，并且每个启用、有会话的实例都已处理到最新已收盘的 bar（输入因子追平、策略自己的消费
// 没有积压、相邻周期的版本记录已补齐），才算追平。追赶期间到达的因子完成事件都按 skipped(catchup) 记录。
// 策略池预热失败的实例要人工修复后重新启用，它的事件被直接确认掉、永远追不平，不参与追赶。
func (d lifecycleDeps) catchup(ctx context.Context, since time.Time) (lifecycle.Catchup, error) {
	out := lifecycle.Catchup{Since: since, CaughtUp: true}
	if !d.ready.Ready() {
		out.CaughtUp, out.BlockedBy, out.Message = false, "模块自检", d.ready.Reason()
		return out, nil
	}
	if d.db == nil {
		return out, nil
	}
	var handler *trigger.Handler
	if d.handler != nil {
		handler = d.handler.Load()
	}
	if handler == nil {
		out.CaughtUp, out.Message = false, "触发处理器尚未启动"
		return out, nil
	}
	enabled := true
	list, err := d.db.ListInstances(ctx, "", &enabled)
	if err != nil {
		out.CaughtUp, out.Message = false, "读取启用的实例失败："+err.Error()
		return out, nil
	}
	now := time.Now().UTC()
	for _, instance := range list {
		if instance.SessionID == nil || *instance.SessionID == "" {
			continue
		}
		if d.warm != nil {
			if status, _ := d.warm.State(instance.InstanceID); status == warmup.StatusFailed {
				continue
			}
		}
		out.Total++
		progress, err := handler.CatchupProgress(ctx, instance.InstanceID, now, catchupGrace)
		message := ""
		switch {
		case err != nil:
			message = "读取追赶进度失败：" + err.Error()
		case progress.CaughtUp():
			out.Done++
		default:
			message = "等待最新已收盘的因子结果（已处理到 " + formatBar(progress.Latest) + "，目标 " + formatBar(progress.Expected) + "）"
		}
		if err == nil && progress.Expected.After(out.Target) {
			out.Target = progress.Expected
		}
		if message != "" && out.CaughtUp {
			out.CaughtUp, out.BlockedBy, out.Message = false, "实例 "+instance.InstanceID, message
		}
	}
	return out, nil
}

func formatBar(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.UTC().Format(time.RFC3339)
}

// triggerGate 把模块状态翻译成处理器的模式：运行中求值（目标是否带影子标记看实例的确认列，由处理器决定），
// 暂停不求值，恢复追赶只记录周期。模块自检没通过（刚启动、出站 EventBus 还在连接或断开）时返回 ModeWait：
// 事件稍后重投，自检通过后照常求值，不丢掉这几期；暂停优先于自检（暂停时不让事件重投）。
func triggerGate(c *lifecycle.Controller, ready *moduleCheck, warm warmState) func(string) trigger.Mode {
	return func(instanceID string) trigger.Mode {
		stage := c.Stage()
		if stage == lifecycle.StageRunning && !ready.Ready() {
			return trigger.ModeWait
		}
		// 策略池预热没完成：事件稍后重投，不丢期。预热失败（要人工修复后重新启用）时不再等：返回 ModeOff。
		// 这一步要放在暂停与追赶判断之前。
		if warm != nil {
			switch status, _ := warm.State(instanceID); status {
			case warmup.StatusRunning:
				return trigger.ModeWait
			case warmup.StatusFailed:
				return trigger.ModeOff
			}
		}
		switch stage {
		case lifecycle.StagePaused:
			return trigger.ModeOff
		case lifecycle.StageResuming:
			return trigger.ModeCatchup // 恢复追赶：只记录周期，不求值、不发目标
		}
		return trigger.ModeLive
	}
}

// releasedAt 把模块最近一次放行的时间交给处理器：放行之前收盘的周期只记录、不求值（系统恢复与单模块恢复一样）。
func releasedAt(c *lifecycle.Controller) func(string) time.Time {
	return func(string) time.Time { return c.ReleasedAt() }
}

func lifecycleRoutes(c *lifecycle.Controller) []health.Route {
	return []health.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: c.CatchupHandler()},
	}
}

// withLifecycle 把模块的生命周期状态放进健康检查响应（Monitor 据此汇总首页的系统状态），不改变就绪判定。
func withLifecycle(snapshot func(context.Context) healthz.Response, c *lifecycle.Controller) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		rsp := snapshot(ctx)
		if rsp.Details == nil {
			rsp.Details = map[string]any{}
		}
		rsp.Details[lifecycleHealthDetailID] = c.State()
		return rsp
	}
}
