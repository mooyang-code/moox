package bootstrap

import (
	"context"
	"path/filepath"
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
}

func newLifecycle(dbPath string, deps lifecycleDeps) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Module:    "strategy",
		StateFile: filepath.Join(filepath.Dir(dbPath), lifecycleStateFileName),
		Hooks: lifecycle.Hooks{
			CheckModule: deps.checkModule,
			CheckObject: deps.checkInstance,
			Objects:     deps.instances,
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
		checks = append(checks, lifecycle.Check{
			Name: "实例健康", Status: lifecycle.CheckFail, Message: "实例处于 " + instance.Health,
			Remedy: "按实例的跳过原因修复后重新启用实例",
		})
	} else {
		checks = append(checks, lifecycle.Check{Name: "实例健康", Status: lifecycle.CheckPass})
	}
	return checks
}

// triggerGate 把控制器的阶段翻译成处理器的模式：运行是正式目标，预热是影子目标，其他都不求值。
func triggerGate(c *lifecycle.Controller) func(string) trigger.Mode {
	return func(instanceID string) trigger.Mode {
		switch c.Stage(instanceRef(instanceID)) {
		case lifecycle.StageRunning:
			return trigger.ModeLive
		case lifecycle.StageWarming:
			return trigger.ModeShadow
		}
		return trigger.ModeOff
	}
}

func lifecycleRoutes(c *lifecycle.Controller) []health.Route {
	return []health.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
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
