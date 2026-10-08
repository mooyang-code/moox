package rpc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	trpc "trpc.group/trpc-go/trpc-go"
)

const demoDSL = `name: demo
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 1}
`

// fakeResolver 按 View 返回固定的解析结果；swap 结尾的 View 视为合约。view 是回放区间校验使用的覆盖范围。
type fakeResolver struct {
	calls   int
	err     error
	columns []string
	loaded  input.Loaded
	loadErr error
	view    input.ViewInfo
}

func (f *fakeResolver) Resolve(_ context.Context, _, viewID string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	f.calls++
	if f.err != nil {
		return input.Resolved{}, nil, f.err
	}
	columns := f.columns
	if columns == nil {
		columns = []string{"close"}
	}
	program, err := dsl.Compile(strategy, columns)
	if err != nil {
		return input.Resolved{}, nil, err
	}
	spot := !strings.HasSuffix(viewID, "swap")
	marketType := "spot"
	if !spot {
		marketType = "swap"
	}
	return input.Resolved{ViewID: viewID, DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar, Spot: spot, MarketType: marketType, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: columns}, program, nil
}

func (f *fakeResolver) LoadLatest(context.Context, string, input.Resolved, *dsl.Program, time.Time) (input.Loaded, error) {
	return f.loaded, f.loadErr
}

func (f *fakeResolver) ReplayWindow(_ context.Context, _ string, resolved input.Resolved, program *dsl.Program, start, end time.Time) (time.Time, error) {
	return input.ReplayWindow(resolved, program, f.view, start, end)
}

// fakeOwner 记录认领与释放；claimErr 让下一次认领失败。
type fakeOwner struct {
	claims      []string
	releases    []string
	claimErr    error
	releaseErr  error
	validateErr error
	owner       map[string]string
}

func (f *fakeOwner) ClaimSession(_ context.Context, _, account, instance, session string) error {
	f.claims = append(f.claims, session)
	if f.claimErr != nil {
		return f.claimErr
	}
	if f.owner == nil {
		f.owner = map[string]string{}
	}
	f.owner[account] = instance + "/" + session
	return nil
}

func (f *fakeOwner) ReleaseSession(_ context.Context, _, account, instance, session string) error {
	f.releases = append(f.releases, session)
	if f.releaseErr != nil {
		return f.releaseErr
	}
	delete(f.owner, account)
	return nil
}

func (f *fakeOwner) ValidateSession(_ context.Context, _, account, instance, session string) error {
	if f.validateErr != nil {
		return f.validateErr
	}
	if f.owner[account] != instance+"/"+session {
		return tradeowner.ErrSessionNotOwned
	}
	return nil
}

type harness struct {
	t        *testing.T
	service  *Service
	resolver *fakeResolver
	owner    *fakeOwner
	ctx      context.Context
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	coverage := input.ViewInfo{ViewID: "view_a", IndexedFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), IndexedTo: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)}
	h := &harness{t: t, resolver: &fakeResolver{view: coverage}, owner: &fakeOwner{}, now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h.service = &Service{Store: repo, Resolver: h.resolver, Owner: h.owner, Now: func() time.Time { return h.now }}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "X-Space-Id", []byte("crypto"))
	h.ctx = ctx
	return h
}

func requireOK(t *testing.T, code int32, msg string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("期望成功，实际 code=%d：%s", code, msg)
	}
}

func requireFail(t *testing.T, code int32, msg, contains string) {
	t.Helper()
	if code == 0 {
		t.Fatalf("期望失败（%s），实际成功", contains)
	}
	if contains != "" && !strings.Contains(msg, contains) {
		t.Fatalf("错误信息应包含 %q：%s", contains, msg)
	}
}

func (h *harness) createStrategy(id, raw string) *strategypb.Strategy {
	h.t.Helper()
	rsp, err := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{StrategyId: id, DslYaml: raw}})
	if err != nil {
		h.t.Fatal(err)
	}
	requireOK(h.t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	return rsp.GetStrategy()
}

func (h *harness) createInstance(id, strategyID, viewID, account string, enabled bool) *strategypb.CreateStrategyInstanceRsp {
	h.t.Helper()
	rsp, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: id, StrategyId: strategyID, ViewId: viewID, LogicalAccountId: account, Enabled: enabled}})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func (h *harness) setEnabled(id string, enabled bool) *strategypb.SetStrategyInstanceEnabledRsp {
	h.t.Helper()
	rsp, err := h.service.SetStrategyInstanceEnabled(h.ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: id, Enabled: enabled})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func TestStrategyDefinitionCRUD(t *testing.T) {
	h := newHarness(t)
	created := h.createStrategy("", demoDSL)
	if created.GetStrategyId() == "" || created.GetName() != "demo" || created.GetDslHash() != dsl.Hash([]byte(demoDSL)) {
		t.Fatalf("创建结果不符：%+v", created)
	}
	invalid, _ := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{DslYaml: "name: x\nrules: []\nbogus: 1\n"}})
	requireFail(t, int32(invalid.GetRetInfo().GetCode()), invalid.GetRetInfo().GetMsg(), "bogus")
	updatedDSL := strings.Replace(demoDSL, "name: demo", "name: demo2", 1)
	updated, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: created.GetStrategyId(), DslYaml: updatedDSL})
	requireOK(t, int32(updated.GetRetInfo().GetCode()), updated.GetRetInfo().GetMsg())
	if updated.GetStrategy().GetName() != "demo2" || updated.GetStrategy().GetDslHash() != dsl.Hash([]byte(updatedDSL)) {
		t.Fatalf("修改结果不符：%+v", updated.GetStrategy())
	}
	listed, _ := h.service.ListStrategies(h.ctx, &strategypb.ListStrategiesReq{})
	if listed.GetTotal() != 1 || listed.GetStrategies()[0].GetName() != "demo2" {
		t.Fatalf("列表不符：%+v", listed)
	}
	missingSpace, _ := h.service.ListStrategies(trpc.BackgroundContext(), &strategypb.ListStrategiesReq{})
	requireFail(t, int32(missingSpace.GetRetInfo().GetCode()), missingSpace.GetRetInfo().GetMsg(), "space_id")
	deleted, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: created.GetStrategyId()})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	if listed, _ := h.service.ListStrategies(h.ctx, &strategypb.ListStrategiesReq{}); listed.GetTotal() != 0 {
		t.Fatalf("删除后列表应为空：%+v", listed)
	}
}

// S14：重启（新的 Service 实例）后沿用会话；重复启用不新建会话；停用再启用新建会话且状态清零。
func TestS14EnableIsIdempotentAndReenableCreatesSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "view_a", "acct-1", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	first := created.GetInstance()
	if !first.GetEnabled() || first.GetSessionId() == "" || first.GetResolvedJson() == "{}" || len(h.owner.claims) != 1 {
		t.Fatalf("创建并启用的实例不符：%+v claims=%v", first, h.owner.claims)
	}
	restarted := &Service{Store: h.service.Store, Resolver: h.resolver, Owner: h.owner, Now: h.service.Now}
	again, _ := restarted.SetStrategyInstanceEnabled(h.ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: "i1", Enabled: true})
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetSessionId() != first.GetSessionId() || len(h.owner.claims) != 1 || h.resolver.calls != 1 {
		t.Fatalf("重复启用不应新建会话或重新解析：%+v claims=%v calls=%d", again.GetInstance(), h.owner.claims, h.resolver.calls)
	}
	if err := restarted.ReconcileEnabledInstances(h.ctx); err != nil {
		t.Fatalf("重启对账应通过：%v", err)
	}
	disabled := h.setEnabled("i1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	if disabled.GetInstance().GetEnabled() || disabled.GetInstance().GetSessionId() != "" || len(h.owner.releases) != 1 {
		t.Fatalf("停用应释放并清空会话：%+v releases=%v", disabled.GetInstance(), h.owner.releases)
	}
	session, err := h.service.Store.GetSession(h.ctx, first.GetSessionId())
	if err != nil || session.ClosedAt == nil {
		t.Fatalf("停用应关闭会话：%+v err=%v", session, err)
	}
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if reenabled.GetInstance().GetSessionId() == first.GetSessionId() || len(h.owner.claims) != 2 {
		t.Fatalf("重新启用应新建会话：%+v", reenabled.GetInstance())
	}
	if _, ok, _ := h.service.Store.LatestOk(h.ctx, "i1", reenabled.GetInstance().GetSessionId()); ok {
		t.Fatal("新会话不应继承旧会话的状态")
	}
	// Trade 侧被改绑后，重复启用必须报错而不是假成功。
	h.owner.owner["acct-1"] = "other/session"
	stale := h.setEnabled("i1", true)
	requireFail(t, int32(stale.GetRetInfo().GetCode()), stale.GetRetInfo().GetMsg(), "不再被 Trade 授权")
}

// 认领失败：确定性拒绝清除待定会话；结果未知保留会话，之后显式启用沿用同一会话。
func TestEnableClaimFailureHandling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		retain bool
	}{
		{"结果未知", errors.New("claim timeout"), true},
		{"确定性拒绝", &tradeowner.ResponseError{Operation: "认领会话", Code: tradepb.ErrorCode(14), Message: "owner conflict"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.createStrategy("s1", demoDSL)
			h.owner.claimErr = tc.err
			rsp := h.createInstance("i1", "s1", "view_a", "acct-1", true)
			requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "i1")
			if rsp.GetInstance().GetInstanceId() != "i1" || rsp.GetInstance().GetEnabled() || (rsp.GetInstance().GetSessionId() != "") != tc.retain {
				t.Fatalf("失败后的实例状态不符：%+v", rsp.GetInstance())
			}
			h.owner.claimErr = nil
			recovered := h.setEnabled("i1", true)
			requireOK(t, int32(recovered.GetRetInfo().GetCode()), recovered.GetRetInfo().GetMsg())
			if (h.owner.claims[0] == h.owner.claims[1]) != tc.retain {
				t.Fatalf("恢复时会话沿用不符：%v", h.owner.claims)
			}
		})
	}
}

// 停用时 Trade 释放失败：保留会话，对账在 Trade 恢复后完成释放。
func TestDisableReleaseFailureIsReconciled(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", true)
	h.owner.releaseErr = errors.New("trade unavailable")
	rsp := h.setEnabled("i1", false)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "释放未确认")
	if rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() == "" {
		t.Fatalf("释放未确认时应已停用并保留会话：%+v", rsp.GetInstance())
	}
	if err := h.service.ReconcileDisabledInstances(h.ctx); err == nil {
		t.Fatal("Trade 仍不可用时对账应返回错误")
	}
	h.owner.releaseErr = nil
	if err := h.service.ReconcileDisabledInstances(h.ctx); err != nil {
		t.Fatal(err)
	}
	instance, _ := h.service.Store.GetInstance(h.ctx, "i1")
	if instance.SessionID != nil {
		t.Fatalf("对账后会话应清空：%+v", instance)
	}
}

// S21（启用部分）：现货 View 上 leverage=1.2 的定义启用被拒绝。
func TestS21EnableRejectsSpotLeverage(t *testing.T) {
	h := newHarness(t)
	levered := `name: levered
rules:
  - id: a
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.6}
  - id: b
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.6}
portfolio:
  leverage: 1.2
`
	h.createStrategy("s1", levered)
	h.resolver.err = errors.New("现货 View 不允许 portfolio.leverage 大于 1（当前 1.2）")
	rsp := h.createInstance("i1", "s1", "view_a", "", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "leverage")
	if rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" {
		t.Fatalf("解析失败不应留下会话：%+v", rsp.GetInstance())
	}
}

func TestCreateInstanceRetrySemantics(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "view_a", "", false)
	requireOK(t, int32(first.GetRetInfo().GetCode()), first.GetRetInfo().GetMsg())
	same := h.createInstance("i1", "s1", "view_a", "", false)
	requireOK(t, int32(same.GetRetInfo().GetCode()), same.GetRetInfo().GetMsg())
	conflict := h.createInstance("i1", "s1", "view_b", "", false)
	requireFail(t, int32(conflict.GetRetInfo().GetCode()), conflict.GetRetInfo().GetMsg(), "冲突")
	enable := h.createInstance("i1", "s1", "view_a", "", true)
	requireFail(t, int32(enable.GetRetInfo().GetCode()), enable.GetRetInfo().GetMsg(), "SetStrategyInstanceEnabled")
	missing := h.createInstance("i2", "nope", "view_a", "", false)
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "不存在")
	other := trpc.BackgroundContext()
	trpc.SetMetaData(other, "X-Space-Id", []byte("other"))
	scoped, _ := h.service.GetStrategyInstance(other, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(scoped.GetRetInfo().GetCode()), scoped.GetRetInfo().GetMsg(), "空间")
}

// S20 / S25：停用、修改定义、重新启用后，旧会话结果仍按旧 DSL 与旧绑定查看；软删除后列表消失、历史仍可查。
func TestS20S25HistoryAfterRedefinitionAndDelete(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "view_a", "", true)
	oldSession := created.GetInstance().GetSessionId()
	result := store.Result{ResultID: "r-old", InstanceID: "i1", SessionID: oldSession, BarEndTime: h.now.Add(time.Hour), ValidUntil: h.now.Add(3 * time.Hour), Status: store.StatusOK, DSLHash: dsl.Hash([]byte(demoDSL)), InputJSON: []byte(`{"view_id":"view_a"}`), TargetsJSON: []byte(`[{"instrument_id":"BTC-USDT","target_weight":"1"}]`), RuleStatesJSON: []byte(`{}`), SummaryJSON: []byte(`{}`), PublishStatus: store.PublishNone, CreatedAt: h.now}
	weight := "1"
	if _, _, err := h.service.Store.CommitResult(h.ctx, store.CommitRequest{Result: result, Items: []store.ResultItem{{RuleID: "r", InstrumentID: "BTC-USDT", Stage: engine.StageWeighted, Weight: &weight}}, Now: h.now}); err != nil {
		t.Fatal(err)
	}
	busy, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "s1", DslYaml: strings.Replace(demoDSL, "name: demo", "name: v2", 1)})
	requireFail(t, int32(busy.GetRetInfo().GetCode()), busy.GetRetInfo().GetMsg(), "启用")
	h.setEnabled("i1", false)
	newDSL := strings.Replace(demoDSL, "name: demo", "name: v2", 1)
	updated, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "s1", DslYaml: newDSL})
	requireOK(t, int32(updated.GetRetInfo().GetCode()), updated.GetRetInfo().GetMsg())
	rebound, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", ViewId: "view_b"}})
	requireOK(t, int32(rebound.GetRetInfo().GetCode()), rebound.GetRetInfo().GetMsg())
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	got, _ := h.service.GetStrategyResult(h.ctx, &strategypb.GetStrategyResultReq{ResultId: "r-old"})
	requireOK(t, int32(got.GetRetInfo().GetCode()), got.GetRetInfo().GetMsg())
	if got.GetDslYaml() != demoDSL || !strings.Contains(got.GetResolvedJson(), `"view_id":"view_a"`) || len(got.GetItems()) != 1 || got.GetItems()[0].GetWeight() != "1" {
		t.Fatalf("旧会话结果应按旧 DSL 与旧绑定展示：dsl=%q resolved=%s items=%+v", got.GetDslYaml(), got.GetResolvedJson(), got.GetItems())
	}
	targets, _ := h.service.ListStrategyTargets(h.ctx, &strategypb.ListStrategyTargetsReq{InstanceId: "i1"})
	if len(targets.GetTargets()) != 0 || targets.GetSessionId() != reenabled.GetInstance().GetSessionId() {
		t.Fatalf("新会话不应返回旧会话的目标：%+v", targets)
	}
	// S25：删除定义被拒绝（仍有未删除实例）；停用并删除实例后成功；历史仍可查。
	refused, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: "s1"})
	requireFail(t, int32(refused.GetRetInfo().GetCode()), refused.GetRetInfo().GetMsg(), "引用")
	enabledDelete, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(enabledDelete.GetRetInfo().GetCode()), enabledDelete.GetRetInfo().GetMsg(), "停用")
	h.setEnabled("i1", false)
	deleted, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	listed, _ := h.service.ListStrategyInstances(h.ctx, &strategypb.ListStrategyInstancesReq{})
	if listed.GetTotal() != 0 {
		t.Fatalf("删除后的实例不应出现在列表：%+v", listed)
	}
	history, _ := h.service.ListStrategyResults(h.ctx, &strategypb.ListStrategyResultsReq{InstanceId: "i1"})
	requireOK(t, int32(history.GetRetInfo().GetCode()), history.GetRetInfo().GetMsg())
	if history.GetTotal() != 1 || history.GetResults()[0].GetResultId() != "r-old" {
		t.Fatalf("删除实例后历史结果仍应可查：%+v", history)
	}
	removed, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: "s1"})
	requireOK(t, int32(removed.GetRetInfo().GetCode()), removed.GetRetInfo().GetMsg())
	restart, _ := h.service.SetStrategyInstanceEnabled(h.ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: "i1", Enabled: true})
	requireFail(t, int32(restart.GetRetInfo().GetCode()), restart.GetRetInfo().GetMsg(), "已删除")
}

// 验收项 4：ValidateStrategy 对引用不存在列的 DSL 返回明确的列名错误；有数据时返回试算结果与解释。
func TestValidateStrategy(t *testing.T) {
	h := newHarness(t)
	plain, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL})
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	h.resolver.err = errors.New("列 momentum_99 不存在于 View view_a；可用列：close")
	missing, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, ViewId: "view_a"})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "momentum_99")
	h.resolver.err = nil
	frame := engine.Frame{BarEnd: h.now, Rows: map[string]engine.Row{"BTC-USDT": {Values: map[string]float64{"close": 2}}, "ETH-USDT": {Values: map[string]float64{"close": 1}}}, Universe: []string{"BTC-USDT", "ETH-USDT"}, Expected: map[string][]string{"r": {"BTC-USDT", "ETH-USDT"}}, AgedOut: map[string][]string{}}
	h.resolver.loaded = input.Loaded{Frame: frame, Boundary: input.PeriodBoundaries{BarEnd: h.now, StorageStart: h.now.Add(-time.Hour)}}
	trial, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, ViewId: "view_a"})
	requireOK(t, int32(trial.GetRetInfo().GetCode()), trial.GetRetInfo().GetMsg())
	if trial.GetTrial().GetStatus() != "ok" || len(trial.GetTrial().GetTargets()) != 1 || trial.GetTrial().GetTargets()[0].GetInstrumentId() != "BTC-USDT" || len(trial.GetTrialItems()) != 2 || trial.GetResolvedJson() == "" {
		t.Fatalf("试算结果不符：%+v", trial)
	}
	h.resolver.loadErr = fmt.Errorf("View view_a 最近三个周期都没有数据")
	empty, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, ViewId: "view_a"})
	requireOK(t, int32(empty.GetRetInfo().GetCode()), empty.GetRetInfo().GetMsg())
	if len(empty.GetDiagnostics()) == 0 || !strings.Contains(empty.GetDiagnostics()[0], "试算未完成") {
		t.Fatalf("没有数据时应给出诊断：%+v", empty.GetDiagnostics())
	}
}

// S26：对源数据集 market_type=swap 的 View 发起回放被拒绝；现货 View 可以发起、查询、取消。
func TestS26StartReplayRejectsSwap(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	swap, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_swap", StartTime: "2026-09-01T00:00:00Z", EndTime: "2026-09-02T00:00:00Z"})
	requireFail(t, int32(swap.GetRetInfo().GetCode()), swap.GetRetInfo().GetMsg(), "只支持现货")
	badRange, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-01T00:00:00Z"})
	requireFail(t, int32(badRange.GetRetInfo().GetCode()), badRange.GetRetInfo().GetMsg(), "晚于")
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-01", EndTime: "2026-09-02", FeeBps: 10})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	if started.GetReplay().GetStatus() != store.ReplayPending || started.GetReplay().GetDslYaml() != demoDSL || started.GetReplay().GetStrategyId() != "s1" {
		t.Fatalf("回放任务不符：%+v", started.GetReplay())
	}
	listed, _ := h.service.ListReplays(h.ctx, &strategypb.ListReplaysReq{})
	if listed.GetTotal() != 1 {
		t.Fatalf("回放列表不符：%+v", listed)
	}
	cancelled, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: started.GetReplay().GetReplayId()})
	requireOK(t, int32(cancelled.GetRetInfo().GetCode()), cancelled.GetRetInfo().GetMsg())
	if cancelled.GetReplay().GetStatus() != store.ReplayCancelled {
		t.Fatalf("取消后的状态不符：%+v", cancelled.GetReplay())
	}
	other := trpc.BackgroundContext()
	trpc.SetMetaData(other, "X-Space-Id", []byte("other"))
	foreign, _ := h.service.GetReplay(other, &strategypb.GetReplayReq{ReplayId: started.GetReplay().GetReplayId()})
	requireFail(t, int32(foreign.GetRetInfo().GetCode()), foreign.GetRetInfo().GetMsg(), "空间")
}

// 停用时 Trade 释放未确认，随后再启用：不能沿用已关闭的旧会话；先在 Trade 侧确认释放，再新建会话并重新解析绑定。
func TestReenableAfterUnconfirmedReleaseCreatesNewSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "view_a", "acct-1", true).GetInstance()
	h.owner.releaseErr = errors.New("trade unavailable")
	disabled := h.setEnabled("i1", false)
	requireFail(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg(), "释放未确认")
	blocked := h.setEnabled("i1", true)
	requireFail(t, int32(blocked.GetRetInfo().GetCode()), blocked.GetRetInfo().GetMsg(), "释放尚未确认")
	if blocked.GetInstance().GetEnabled() || blocked.GetInstance().GetSessionId() != first.GetSessionId() {
		t.Fatalf("释放未确认时不能启用，也不能丢掉待释放的会话：%+v", blocked.GetInstance())
	}
	h.owner.releaseErr = nil
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if reenabled.GetInstance().GetSessionId() == first.GetSessionId() {
		t.Fatalf("不能沿用已关闭的会话：%+v", reenabled.GetInstance())
	}
	if h.resolver.calls != 2 {
		t.Fatalf("新会话应重新解析绑定：calls=%d", h.resolver.calls)
	}
	released := false
	for _, session := range h.owner.releases {
		released = released || session == first.GetSessionId()
	}
	if !released {
		t.Fatalf("旧会话应在 Trade 侧释放：%v", h.owner.releases)
	}
}

// 启动对账只隔离不一致的实例：Trade 明确拒绝的会话自动停用；结果未知时保持启用并标记 degraded。
func TestReconcileEnabledInstancesIsolatesInstances(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", true)
	h.createInstance("i2", "s1", "view_a", "acct-2", true)
	h.owner.owner["acct-1"] = "other/session"
	err := h.service.ReconcileEnabledInstances(h.ctx)
	if err == nil || !strings.Contains(err.Error(), "已自动停用") {
		t.Fatalf("被拒绝的会话应自动停用并告警：%v", err)
	}
	rejected, _ := h.service.Store.GetInstance(h.ctx, "i1")
	if rejected.Enabled || rejected.SessionID != nil || rejected.Health != store.HealthDegraded {
		t.Fatalf("i1 应已停用、清空会话并标记 degraded：%+v", rejected)
	}
	if healthy, _ := h.service.Store.GetInstance(h.ctx, "i2"); !healthy.Enabled {
		t.Fatalf("i2 不应受影响：%+v", healthy)
	}
	h.owner.validateErr = &tradeowner.TransportError{Operation: "校验会话", Err: errors.New("dial tcp 10.0.0.1:443: connect refused")}
	err = h.service.ReconcileEnabledInstances(h.ctx)
	if err == nil || !strings.Contains(err.Error(), "暂时无法核实") || strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("结果未知时只告警，且不暴露网关地址：%v", err)
	}
	if unknown, _ := h.service.Store.GetInstance(h.ctx, "i2"); !unknown.Enabled || unknown.Health != store.HealthSessionUnverified {
		t.Fatalf("i2 应保持启用并标记 session_unverified：%+v", unknown)
	}
	// 再次核实成功后恢复 ok；ok 周期不会提前清除 session_unverified。
	h.owner.validateErr = nil
	if err := h.service.ReconcileEnabledInstances(h.ctx); err != nil {
		t.Fatalf("核实成功不应报错：%v", err)
	}
	if recovered, _ := h.service.Store.GetInstance(h.ctx, "i2"); recovered.Health != store.HealthOK {
		t.Fatalf("核实成功后应恢复 ok：%+v", recovered)
	}
}

// 存储约束错误以中文概述返回，不暴露表名、列名与约束原文。
func TestStoreErrorsAreNormalized(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("dup", demoDSL)
	rsp, _ := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{StrategyId: "dup", DslYaml: demoDSL}})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "记录已存在")
	if message := rsp.GetRetInfo().GetMsg(); strings.Contains(message, "t_strategy") || strings.Contains(message, "UNIQUE") {
		t.Fatalf("不应暴露存储细节：%s", message)
	}
}

// StartReplay：定义与 DSL 必须二选一；起点在提交时同步校验；终点截到 View 最新一根后保存。
func TestStartReplayValidatesWindowSynchronously(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	both, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", DslYaml: demoDSL, ViewId: "view_a", StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg(), "只能给出其一")
	neither, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{ViewId: "view_a", StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(neither.GetRetInfo().GetCode()), neither.GetRetInfo().GetMsg(), "只能给出其一")
	h.resolver.view = input.ViewInfo{ViewID: "view_a", IndexedFrom: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), IndexedTo: time.Date(2026, 9, 3, 5, 0, 0, 0, time.UTC)}
	early, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(early.GetRetInfo().GetCode()), early.GetRetInfo().GetMsg(), "可用起点")
	future, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02", EndTime: "2026-09-30"})
	requireOK(t, int32(future.GetRetInfo().GetCode()), future.GetRetInfo().GetMsg())
	// 最新一根的 bar_start 是 09-03 05:00，终点截到它的 bar_end（06:00）：落库往返后仍包含这一根。
	if end := future.GetReplay().GetEndTime(); end != "2026-09-03T06:00:00Z" {
		t.Fatalf("终点应截到最新一根的 bar_end：%s", end)
	}
	if future.GetBarCount() != 30 || future.GetFirstBarEnd() != "2026-09-02T01:00:00Z" || future.GetLastBarEnd() != "2026-09-03T06:00:00Z" {
		t.Fatalf("应返回实际回放的根数与首末根：%d %s %s", future.GetBarCount(), future.GetFirstBarEnd(), future.GetLastBarEnd())
	}
	stored, err := h.service.Store.GetReplay(h.ctx, future.GetReplay().GetReplayId())
	if err != nil {
		t.Fatal(err)
	}
	bars, err := input.ReplayBars(input.DefaultCalendar, "1h", stored.StartTime, stored.EndTime, input.DefaultReplayMaxBars)
	if err != nil || len(bars) != 30 {
		t.Fatalf("落库往返后仍应是 30 根：%d err=%v", len(bars), err)
	}
	badFee, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02", EndTime: "2026-09-03", FeeBps: 1500})
	requireFail(t, int32(badFee.GetRetInfo().GetCode()), badFee.GetRetInfo().GetMsg(), "fee_bps")
	h.resolver.view.IndexedFrom = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	tooLong, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2023-01-02", EndTime: "2026-09-30"})
	requireFail(t, int32(tooLong.GetRetInfo().GetCode()), tooLong.GetRetInfo().GetMsg(), "20000")
}

// Trade 对释放返回账户不存在（code=5）也是确定结论：停用后会话照常清空，之后可以删除或重新启用。
func TestDisableTreatsMissingAccountAsReleased(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", true)
	h.owner.releaseErr = &tradeowner.ResponseError{Operation: "释放会话", Code: tradepb.ErrorCode(5), Message: "account not found"}
	rsp := h.setEnabled("i1", false)
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" {
		t.Fatalf("账户不存在时停用应清空会话：%+v", rsp.GetInstance())
	}
}

// 同一空间的组合账户已被另一个启用实例绑定时，启用在联系 Trade 认领之前就被拒绝。
func TestEnableRejectsAccountHeldByAnotherInstance(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", true)
	claims := len(h.owner.claims)
	second := h.createInstance("i2", "s1", "view_a", "acct-1", true)
	requireFail(t, int32(second.GetRetInfo().GetCode()), second.GetRetInfo().GetMsg(), "已被启用实例 i1 绑定")
	if len(h.owner.claims) != claims {
		t.Fatalf("不应向 Trade 认领：%v", h.owner.claims)
	}
}

// 不存在的 ID 返回中文的"记录不存在"，不透出 ORM 的英文原文。
func TestNotFoundIsReportedInChinese(t *testing.T) {
	h := newHarness(t)
	rsp, _ := h.service.GetStrategy(h.ctx, &strategypb.GetStrategyReq{StrategyId: "missing"})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "记录不存在")
	if strings.Contains(rsp.GetRetInfo().GetMsg(), "record not found") {
		t.Fatalf("不应透出英文原文：%s", rsp.GetRetInfo().GetMsg())
	}
}
