package sysctl

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// resetAPI 把真实的生命周期控制器当作模块的重置接口。
type resetAPI struct{ c *lifecycle.Controller }

func (a resetAPI) ResetPlan(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlan, error) {
	return a.c.ResetPlan(ctx, scope)
}
func (a resetAPI) ResetApply(ctx context.Context, req lifecycle.ResetApplyRequest) (lifecycle.ResetStatus, error) {
	return a.c.ResetApply(ctx, req)
}
func (a resetAPI) ResetStatus(context.Context) (lifecycle.ResetStatus, error) {
	return a.c.ResetStatus(), nil
}

var statsCounter atomic.Int64

type resetFixture struct {
	o        *Orchestrator
	ran      *callLog
	blockers map[string]*atomic.Value // 每个模块的阻止条件
	failOnce map[string]*atomic.Bool  // 让模块的 clear 步骤失败一次
	extra    map[string]*atomic.Bool  // 让计划多一个步骤（模拟 plan 之后表结构变化）
	ctrls    map[string]*lifecycle.Controller
	mu       sync.Mutex
}

func newResetFixture(t *testing.T, production bool, external []ExternalStep, names ...string) *resetFixture {
	t.Helper()
	f := &resetFixture{ran: &callLog{}, blockers: map[string]*atomic.Value{}, failOnce: map[string]*atomic.Bool{}, extra: map[string]*atomic.Bool{}, ctrls: map[string]*lifecycle.Controller{}}
	var mods []Module
	for _, name := range names {
		name := name
		bl := &atomic.Value{}
		bl.Store([]string(nil))
		fail, extra := &atomic.Bool{}, &atomic.Bool{}
		f.blockers[name], f.failOnce[name], f.extra[name] = bl, fail, extra
		step := func(stepName string) lifecycle.ResetStep {
			return lifecycle.ResetStep{Name: stepName, Describe: stepName, Run: func(context.Context, string) error {
				if stepName == "clear" && fail.CompareAndSwap(true, false) {
					return context.DeadlineExceeded
				}
				f.ran.add(name + ":" + stepName)
				return nil
			}}
		}
		c, err := lifecycle.New(lifecycle.Options{Module: name, StateFile: filepath.Join(t.TempDir(), "lifecycle.json"), Hooks: lifecycle.Hooks{
			Reset: &lifecycle.ResetSpec{Plan: func(context.Context, lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
				steps := []lifecycle.ResetStep{step("backup"), step("clear")}
				if extra.Load() {
					steps = append(steps, step("seed"))
				}
				return lifecycle.ResetPlanInfo{Blockers: bl.Load().([]string), Stats: map[string]any{"rows": statsCounter.Add(1)}}, steps, nil
			}},
		}})
		require.NoError(t, err)
		c.Refresh(context.Background())
		f.ctrls[name] = c
		mods = append(mods, Module{Name: name, API: &controllerAPI{c: c, log: f.ran, name: name}, Reset: resetAPI{c: c}})
	}
	f.o = &Orchestrator{
		Modules: mods, By: "test", Poll: time.Millisecond,
		Sleep: func(ctx context.Context, d time.Duration) error { time.Sleep(d); return ctx.Err() },
		Reset: ResetConfig{StateDir: t.TempDir(), SystemName: "moox-prod", Production: production, External: external},
	}
	return f
}

var resetNames = []string{Collector, Strategy, Trade, Monitor}

// 完整流程：生产环境要输入系统名；外部步骤停下等人工；模块按下游到上游重置，外部步骤排在指定模块之后；令牌只能用一次。
func TestSystemResetFullFlow(t *testing.T) {
	ctx := context.Background()
	storage := ExternalStep{Name: "storage", Describe: "Storage 重置", After: Collector, Commands: []string{"moox-cli storage reset-metadata --yes"}}
	f := newResetFixture(t, true, []ExternalStep{storage}, resetNames...)

	plan, err := f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.NotEmpty(t, plan.Token)
	require.Len(t, plan.Modules, 4)
	require.Equal(t, []string{"storage"}, []string{plan.External[0].Name})
	require.True(t, plan.External[0].Manual)

	_, err = f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.ErrorContains(t, err, "系统名", "生产环境必须输入系统名")
	_, err = f.o.ApplyReset(ctx, ResetApplyRequest{Token: "bad", ConfirmName: "moox-prod"})
	require.ErrorContains(t, err, "令牌")

	j, err := f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token, ConfirmName: "moox-prod"})
	require.ErrorIs(t, err, ErrAwaitingExternal)
	require.Equal(t, "waiting_external", j.State)
	var steps []string
	for _, s := range j.Steps {
		steps = append(steps, s.Name+":"+s.State)
	}
	require.Equal(t, []string{"strategy:done", "trade:done", "collector:done", "storage:awaiting", "monitor:pending"}, steps)

	_, err = f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token, ConfirmName: "moox-prod"})
	require.ErrorContains(t, err, "未完成", "令牌用过之后不能再发起")

	j, err = f.o.ApplyReset(ctx, ResetApplyRequest{OperationID: j.OperationID, ExternalDone: []string{"storage"}})
	require.NoError(t, err)
	require.Equal(t, "done", j.State)

	// 模块内的步骤顺序：下游到上游，Monitor 最后；每个模块先备份再清理。
	var ran []string
	for _, c := range f.ran.snapshot() {
		if c[len(c)-6:] == "backup" || c[len(c)-5:] == "clear" {
			ran = append(ran, c)
		}
	}
	require.Equal(t, []string{
		"strategy:backup", "strategy:clear", "trade:backup", "trade:clear",
		"collector:backup", "collector:clear", "monitor:backup", "monitor:clear",
	}, ran)
	for name, c := range f.ctrls {
		require.Equal(t, lifecycle.StageUninitialized, c.Snapshot().Self.Stage, name)
		require.NotNil(t, c.ResetStatus().LastReset, name)
	}
	// 完成之后可以再做新的计划。
	_, err = f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
}

// 某个模块失败：停在那里，后面的不动；修好后凭系统 operation_id 续做，已完成的模块不重做。
func TestSystemResetResumeAfterModuleFailure(t *testing.T) {
	ctx := context.Background()
	f := newResetFixture(t, false, nil, resetNames...)
	f.failOnce[Collector].Store(true)
	plan, err := f.o.PlanReset(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	j, err := f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.ErrorContains(t, err, "collector")
	require.Equal(t, "failed", j.State)
	require.Equal(t, lifecycle.StageResetFailed, f.ctrls[Collector].Snapshot().Self.Stage)
	require.Equal(t, lifecycle.StagePaused, f.ctrls[Monitor].Snapshot().Self.Stage, "后面的模块已暂停但没重置")
	before := len(f.ran.snapshot())

	j, err = f.o.ApplyReset(ctx, ResetApplyRequest{OperationID: j.OperationID})
	require.NoError(t, err)
	require.Equal(t, "done", j.State)
	var strategyRuns int
	for _, c := range f.ran.snapshot() {
		if c == "strategy:backup" {
			strategyRuns++
		}
	}
	require.Equal(t, 1, strategyRuns, "已完成的模块不重做")
	require.Greater(t, len(f.ran.snapshot()), before)
}

// 阻止条件：任一模块有阻止条件就不发令牌；模块计划在 plan 之后变化，apply 在那个模块上停下且之前的模块不受影响。
func TestSystemResetBlockersAndPlanDrift(t *testing.T) {
	ctx := context.Background()
	f := newResetFixture(t, false, nil, resetNames...)
	f.blockers[Trade].Store([]string{"实盘账户 A 还有持仓"})
	plan, err := f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Empty(t, plan.Token)
	require.Equal(t, []string{"trade：实盘账户 A 还有持仓"}, plan.Blockers)
	_, err = f.o.ApplyReset(ctx, ResetApplyRequest{})
	require.Error(t, err)

	f.blockers[Trade].Store([]string(nil))
	plan, err = f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Token)
	f.extra[Collector].Store(true) // plan 之后 Collector 的计划多了一步
	j, err := f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.ErrorContains(t, err, "不一致")
	require.Equal(t, "failed", j.State)
	require.Equal(t, lifecycle.StageUninitialized, f.ctrls[Strategy].Snapshot().Self.Stage, "之前的模块已重置完成")
	require.Equal(t, "failed", j.Steps[2].State)
}

// 有自动执行的外部步骤时不停；过期令牌被拒绝；没配置重置接口的模块给出明确原因。
func TestSystemResetAutoExternalExpiryAndMissingAPI(t *testing.T) {
	ctx := context.Background()
	var ran atomic.Bool
	auto := ExternalStep{Name: "admin", Describe: "Admin 业务部分", Run: func(context.Context) error { ran.Store(true); return nil }}
	f := newResetFixture(t, false, []ExternalStep{auto}, Strategy, Monitor)
	now := time.Now()
	f.o.Reset.Now = func() time.Time { return now }
	plan, err := f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.False(t, plan.External[0].Manual)
	now = now.Add(lifecycle.ResetTokenTTL + time.Minute)
	_, err = f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.ErrorContains(t, err, "过期")

	now = time.Now()
	plan, err = f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	j, err := f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.NoError(t, err)
	require.Equal(t, "done", j.State)
	require.True(t, ran.Load())

	g := newResetFixture(t, false, nil, Strategy)
	g.o.Modules[0].Reset = nil
	plan, err = g.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Contains(t, plan.Blockers[0], "重置专用密钥")
}

// 阻止条件是暂时性的（例如 SCF 定时器刚暂停还没确认停用）：在时限内消失就继续，超时仍在才失败；统计值每次都变也不影响摘要。
func TestSystemResetWaitsForTransientBlockers(t *testing.T) {
	ctx := context.Background()
	f := newResetFixture(t, false, nil, Strategy, Collector)
	f.o.Reset.BlockerWait = time.Minute
	plan, err := f.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)

	// Collector 在 plan 之后出现阻止条件，两次轮询后消失。
	var polls atomic.Int32
	f.blockers[Collector].Store([]string{"定时器还没有确认停用"})
	f.o.Sleep = func(ctx context.Context, d time.Duration) error {
		if polls.Add(1) == 2 {
			f.blockers[Collector].Store([]string(nil))
		}
		return ctx.Err()
	}
	j, err := f.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.NoError(t, err)
	require.Equal(t, "done", j.State)
	require.GreaterOrEqual(t, polls.Load(), int32(2))

	// 一直不消失：超时失败，信息里带阻止条件。
	g := newResetFixture(t, false, nil, Strategy)
	g.o.Reset.BlockerWait = time.Millisecond
	plan, err = g.o.PlanReset(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	g.blockers[Strategy].Store([]string{"有进行中的运行"})
	_, err = g.o.ApplyReset(ctx, ResetApplyRequest{Token: plan.Token})
	require.ErrorContains(t, err, "有进行中的运行")
}
