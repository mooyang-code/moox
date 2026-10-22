package bootstrap

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 策略模块的生命周期（设计第 6 节）：对象是启用的策略实例，只有处于预热或运行的实例才求值。
// 预热：求值并发出带影子标记的目标（Trade 只换算记录，不下单）；暂停或没有启动：不求值、不发新目标，持仓不动。

const (
	instanceKind            = "strategy_instance"
	lifecycleRefreshEvery   = 30 * time.Second
	lifecycleStateFileName  = "lifecycle.json"
	lifecycleHealthDetailID = "lifecycle"
)

// lifecycleDeps 是生命周期检查依赖的模块状态。
type lifecycleDeps struct {
	db             *store.Store
	eventConnected func() bool
	consumerReady  func() bool
	// handler 在生命周期之后才创建（它的门禁依赖控制器），创建后绑定；追赶进度从它读取。
	handler *atomic.Pointer[trigger.Handler]
}

// catchupGrace 是恢复追赶目标之前留的宽限：最新一根刚收盘时因子结果还在路上，不把它算进追赶目标。
const catchupGrace = time.Minute

func newLifecycle(dbPath string, deps lifecycleDeps) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Module:    "strategy",
		StateFile: filepath.Join(filepath.Dir(dbPath), lifecycleStateFileName),
		Hooks: lifecycle.Hooks{
			CheckModule: deps.checkModule,
			CheckObject: deps.checkInstance,
			Objects:     deps.instances,
			Catchup:     deps.catchup,
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("strategy_lifecycle_changed kind=%s id=%s from=%s to=%s reason=%s", ref.Kind, ref.ID, from, to, reason)
			},
		},
	})
}

func instanceRef(id string) lifecycle.Ref { return lifecycle.Ref{Kind: instanceKind, ID: id} }

func (d lifecycleDeps) instances() []lifecycle.Ref {
	enabled := true
	list, err := d.db.ListInstances(context.Background(), "", &enabled)
	if err != nil {
		return nil
	}
	refs := make([]lifecycle.Ref, 0, len(list))
	for _, instance := range list {
		refs = append(refs, instanceRef(instance.InstanceID))
	}
	return refs
}

// catchup 报告实例的恢复追赶进度：追赶期间到达的因子完成事件都按 skipped(catchup) 记录，已处理到最新已收盘的 bar 就是追平
// （输入因子追平、策略自己的消费没有积压、相邻周期的版本记录已补齐）。模块自身没有数据要追赶。
func (d lifecycleDeps) catchup(ctx context.Context, ref lifecycle.Ref, since time.Time) (lifecycle.Catchup, error) {
	out := lifecycle.Catchup{Since: since, CaughtUp: true}
	if ref == (lifecycle.Ref{}) {
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
	progress, err := handler.CatchupProgress(ctx, ref.ID, time.Now().UTC(), catchupGrace)
	if err != nil {
		out.CaughtUp, out.Message = false, "读取追赶进度失败："+err.Error()
		return out, nil
	}
	out.Target = progress.Expected
	out.CaughtUp = progress.CaughtUp()
	if !out.CaughtUp {
		out.BlockedBy = "输入因子"
		out.Message = "等待最新已收盘的因子结果（已处理到 " + formatBar(progress.Latest) + "，目标 " + formatBar(progress.Expected) + "）"
	}
	return out, nil
}

func formatBar(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.UTC().Format(time.RFC3339)
}

func (d lifecycleDeps) checkModule(ctx context.Context) []lifecycle.Check {
	check := func(name string, ok bool, message string) lifecycle.Check {
		if ok {
			return lifecycle.Check{Name: name, Status: lifecycle.CheckPass}
		}
		return lifecycle.Check{Name: name, Status: lifecycle.CheckFail, Message: message}
	}
	return []lifecycle.Check{
		check("数据库", d.db != nil && d.db.Ping(ctx) == nil, "数据库不可用"),
		check("EventBus", d.eventConnected == nil || d.eventConnected(), "出站 EventBus 未连接"),
		check("就绪事件消费者", d.consumerReady == nil || d.consumerReady(), "就绪事件消费者未连接"),
	}
}

// checkInstance 检查一个实例能不能求值：实例仍然启用、有会话、没有被标记为需要人工处理。
func (d lifecycleDeps) checkInstance(ctx context.Context, ref lifecycle.Ref) []lifecycle.Check {
	instance, err := d.db.GetInstance(ctx, ref.ID)
	if err != nil || !instance.Enabled || instance.SessionID == nil || *instance.SessionID == "" {
		return []lifecycle.Check{{Name: "实例", Status: lifecycle.CheckFail, Message: "实例未启用或没有会话"}}
	}
	checks := []lifecycle.Check{{Name: "实例", Status: lifecycle.CheckPass}}
	if instance.Health != "ok" {
		// 只告警、不挡路：实例健康由成功求值恢复；这里判失败会让实例自动暂停、不再求值，健康就永远恢复不了（循环依赖）。
		checks = append(checks, lifecycle.Check{
			Name: "实例健康", Status: lifecycle.CheckWarn, Message: "实例处于 " + instance.Health,
			Remedy: "按实例的跳过原因修复（补数据、改配置）；因子定义变化要重新启用实例",
		})
	} else {
		checks = append(checks, lifecycle.Check{Name: "实例健康", Status: lifecycle.CheckPass})
	}
	return checks
}

// triggerGate 把控制器的阶段翻译成处理器的模式：运行是正式目标，预热是影子目标，其他都不求值
// （暂停、恢复追赶、重置、故障，以及模块自身未就绪，对象各自的阶段不变但都不求值）。
func triggerGate(c *lifecycle.Controller) func(string) trigger.Mode {
	return func(instanceID string) trigger.Mode {
		if !c.Allowed(instanceRef(instanceID)) { // 模块自身暂停或故障时，所有实例都不求值
			if c.CatchupAllowed(instanceRef(instanceID)) {
				return trigger.ModeCatchup // 恢复追赶：只记录周期，不求值、不发目标
			}
			return trigger.ModeOff
		}
		switch c.Stage(instanceRef(instanceID)) {
		case lifecycle.StageRunning:
			return trigger.ModeLive
		case lifecycle.StageWarming:
			return trigger.ModeShadow
		}
		return trigger.ModeOff
	}
}

// releasedAt 把控制器记录的放行时间交给处理器：放行之前收盘的周期只记录、不求值。
func releasedAt(c *lifecycle.Controller) func(string) time.Time {
	return func(instanceID string) time.Time { return c.ReleasedAt(instanceRef(instanceID)) }
}

func lifecycleRoutes(c *lifecycle.Controller) []health.Route {
	return []health.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: c.CatchupHandler()},
	}
}

// withLifecycle 把生命周期快照放进健康检查响应（Monitor 据此汇总），生命周期的检查本身不改变就绪判定。
func withLifecycle(snapshot func(context.Context) healthz.Response, c *lifecycle.Controller) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		rsp := snapshot(ctx)
		if rsp.Details == nil {
			rsp.Details = map[string]any{}
		}
		rsp.Details[lifecycleHealthDetailID] = c.Compact()
		return rsp
	}
}
