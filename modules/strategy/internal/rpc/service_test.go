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

// fakeResolver 按因子任务返回固定的解析结果；swap 结尾的任务视为合约。view 是回放区间校验使用的覆盖范围。
type fakeResolver struct {
	calls         int
	taskMarkets   map[string]string
	taskMarketErr error
	// resolvedTasks 记下最近一次 Resolve 收到的任务（按市场先后）。
	resolvedTasks []string
	bound         []string
	err           error
	columns       []string
	loaded        input.Loaded
	loadErr       error
	view          input.ViewInfo
	coverageErr   error
	// onResolve 在每次解析开始时调用，用来模拟解析期间的并发修改。
	onResolve func()
	// calendar 非空时解析结果使用该日历的日线（例如 cn_stock）。
	calendar string
	// series 是“因子现算”提交校验返回的主帧序列数（为 0 时按 3 个标的 × 市场数）。
	series int
}

// TaskMarket：后缀 swap 的任务是永续，其余现货；taskMarkets 里登记的优先。
func (f *fakeResolver) TaskMarket(_ context.Context, _, taskID string) (string, error) {
	if market, ok := f.taskMarkets[taskID]; ok {
		return market, nil
	}
	if f.taskMarketErr != nil {
		return "", f.taskMarketErr
	}
	if strings.HasSuffix(taskID, "swap") {
		return "swap", nil
	}
	return "spot", nil
}

func (f *fakeResolver) CheckCoverage(context.Context, string, input.Resolved, time.Time) error {
	return f.coverageErr
}

func (f *fakeResolver) Resolve(_ context.Context, _ string, registered map[string]string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	taskIDs := input.TaskIDs(registered)
	taskID := taskIDs[0]
	f.resolvedTasks = taskIDs
	f.calls++
	if f.onResolve != nil {
		f.onResolve()
	}
	if f.err != nil {
		return input.Resolved{}, nil, f.err
	}
	columns := f.columns
	if columns == nil {
		columns = []string{"close"}
	}
	// 一个任务：后缀 swap 的是永续，其余现货；两个任务：现货、永续各一个，市场模式是 both/spot。
	mode := dsl.SingleMarket("spot")
	if strings.HasSuffix(taskID, "swap") {
		mode = dsl.SingleMarket("swap")
	}
	tasks := map[string]input.TaskBinding{}
	for i, id := range taskIDs {
		datasetID, err := input.DatasetIDOfTask(id)
		if err != nil {
			return input.Resolved{}, nil, err
		}
		market := mode.Select
		if len(taskIDs) == 2 {
			market = []string{"spot", "swap"}[i]
			mode = dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}
		}
		tasks[market] = input.TaskBinding{FactorTaskID: id, DatasetID: datasetID}
	}
	strategy.Market = mode
	program, err := dsl.Compile(strategy, columns)
	if err != nil {
		return input.Resolved{}, nil, err
	}
	resolved := input.Resolved{Market: mode, Tasks: tasks, Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: columns}
	if f.calendar != "" {
		resolved.Calendar, resolved.Bar = f.calendar, "1d"
	}
	return resolved, program, nil
}

// ResolveInstance 记下启用时关联任务的实例，其余与 Resolve 相同。
func (f *fakeResolver) ResolveInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	taskIDs := input.TaskIDs(tasks)
	f.bound = append(f.bound, instanceID+"@"+taskIDs[0])
	return f.Resolve(ctx, spaceID, tasks, strategy)
}

func (f *fakeResolver) LoadLatest(context.Context, string, input.Resolved, *dsl.Program, time.Time) (input.Loaded, error) {
	return f.loaded, f.loadErr
}

func (f *fakeResolver) ReplayWindow(_ context.Context, _ string, resolved input.Resolved, program *dsl.Program, start, end, now time.Time) (time.Time, map[string]string, error) {
	end, err := input.ReplayWindow(resolved, program, f.view, start, end, now)
	generations := make(map[string]string, len(resolved.Tasks))
	for market := range resolved.Tasks {
		generations[market] = f.view.Generation
	}
	return end, generations, err
}

// ComputeReplayWindow 只把终点截到 now 之前的整点，代次取 view 的，序列数取 series（为 0 时按 3 个标的 × 市场数）。
func (f *fakeResolver) ComputeReplayWindow(_ context.Context, _ string, resolved input.Resolved, start, end, now time.Time) (time.Time, map[string]string, int, error) {
	if limit := now.Truncate(time.Hour).Add(-time.Hour); end.After(limit) {
		end = limit
	}
	if !end.After(start) {
		return time.Time{}, nil, 0, input.ErrNoCompleteBar
	}
	generations := make(map[string]string, len(resolved.Tasks))
	for key := range resolved.AllTaskBindings() {
		generations[key] = f.view.Generation
	}
	series := f.series
	if series == 0 {
		series = 3 * len(resolved.Markets())
	}
	return end, generations, series, nil
}

// closeCall 是一次 CloseTargetSession：组合账户、实例、会话与开始时刻。
type closeCall struct {
	account, instance, session string
	startedAt                  int64
}

// fakeTrade 记录启用前的账户核对与停用后的关闭会话；checkErr、closeErr 让之后的调用失败。
type fakeTrade struct {
	checks   []string
	closes   []closeCall
	checkErr error
	closeErr error
}

func (f *fakeTrade) CheckAccount(_ context.Context, _, account string, _ []string) error {
	f.checks = append(f.checks, account)
	return f.checkErr
}

func (f *fakeTrade) CloseSession(_ context.Context, _, account, instance, session string, startedAt int64) error {
	f.closes = append(f.closes, closeCall{account: account, instance: instance, session: session, startedAt: startedAt})
	return f.closeErr
}

type harness struct {
	t        *testing.T
	service  *Service
	resolver *fakeResolver
	trade    *fakeTrade
	ctx      context.Context
	now      time.Time
}

// ms 把测试里的 RFC3339 时刻或日期（UTC 零点）换成接口用的 Unix 毫秒。
func ms(text string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if at, err := time.Parse(layout, text); err == nil {
			return at.UnixMilli()
		}
	}
	panic("无法解析的测试时刻：" + text)
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
	coverage := input.ViewInfo{DatasetID: "dataset_factor_a", ActiveIndexID: "idx", Generation: "idx@b1", IndexedFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), IndexedTo: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)}
	h := &harness{t: t, resolver: &fakeResolver{view: coverage}, trade: &fakeTrade{}, now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h.service = &Service{Store: repo, Resolver: h.resolver, Trade: h.trade, Now: func() time.Time { return h.now }}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "x-space-id", []byte("crypto"))
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

// taskMap 把测试里的单个因子任务 ID 变成请求的 factor_tasks（现货）；空串表示不给。
func taskMap(taskID string) map[string]string {
	if taskID == "" {
		return nil
	}
	return map[string]string{"spot": taskID}
}

func (h *harness) createInstance(id, strategyID, taskID, account string, enabled bool) *strategypb.CreateStrategyInstanceRsp {
	h.t.Helper()
	rsp, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: id, StrategyId: strategyID, FactorTasks: taskMap(taskID), LogicalAccountId: account, Enabled: enabled}})
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

// S14：重启（新的 Service 实例）后沿用会话；重复启用不新建会话、不联系 Trade；停用再启用新建会话（规则状态是否沿用见 store 的 carryState）。
func TestS14EnableIsIdempotentAndReenableCreatesSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	first := created.GetInstance()
	if !first.GetEnabled() || first.GetSessionId() == "" || first.GetResolvedJson() == "{}" || len(h.trade.checks) != 1 || h.trade.checks[0] != "acct-1" {
		t.Fatalf("创建并启用的实例不符：%+v checks=%v", first, h.trade.checks)
	}
	restarted := &Service{Store: h.service.Store, Resolver: h.resolver, Trade: h.trade, Now: h.service.Now}
	again, _ := restarted.SetStrategyInstanceEnabled(h.ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: "i1", Enabled: true})
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetSessionId() != first.GetSessionId() || len(h.trade.checks) != 1 || len(h.trade.closes) != 0 || h.resolver.calls != 1 {
		t.Fatalf("重复启用不应新建会话、重新解析或联系 Trade：%+v checks=%v calls=%d", again.GetInstance(), h.trade.checks, h.resolver.calls)
	}
	disabled := h.setEnabled("i1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	session, err := h.service.Store.GetSession(h.ctx, first.GetSessionId())
	if err != nil || session.ClosedAt == nil {
		t.Fatalf("停用应关闭会话：%+v err=%v", session, err)
	}
	want := closeCall{account: "acct-1", instance: "i1", session: first.GetSessionId(), startedAt: session.StartedAt}
	if disabled.GetInstance().GetEnabled() || disabled.GetInstance().GetSessionId() != "" || len(h.trade.closes) != 1 || h.trade.closes[0] != want {
		t.Fatalf("停用应清空会话并按会话通知 Trade 关闭：%+v closes=%v want=%v", disabled.GetInstance(), h.trade.closes, want)
	}
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if reenabled.GetInstance().GetSessionId() == first.GetSessionId() || len(h.trade.checks) != 2 {
		t.Fatalf("重新启用应新建会话并再核对一次账户：%+v checks=%v", reenabled.GetInstance(), h.trade.checks)
	}
	if _, ok, _ := h.service.Store.LatestOk(h.ctx, "i1", reenabled.GetInstance().GetSessionId()); ok {
		t.Fatal("新会话不应继承旧会话的状态")
	}
	if next, _ := h.service.Store.GetSession(h.ctx, reenabled.GetInstance().GetSessionId()); next.StartedAt <= session.StartedAt {
		t.Fatalf("新会话的开始时刻应晚于旧会话：%d <= %d", next.StartedAt, session.StartedAt)
	}
}

// 启用前读账户核对（人工控制、市场不符、Trade 不可达）失败时不写会话、实例保持停用；之后重试照常启用。
func TestEnableAccountCheckFailureLeavesNoSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Trade 不可达", errors.New("Trade 账户读取失败：Trade 或网关不可达，结果未知")},
		{"人工控制", tradeowner.ErrManualControl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.createStrategy("s1", demoDSL)
			h.trade.checkErr = tc.err
			rsp := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true)
			requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "i1")
			if rsp.GetInstance().GetInstanceId() != "i1" || rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" {
				t.Fatalf("失败后的实例状态不符：%+v", rsp.GetInstance())
			}
			if sessions, err := h.service.Store.ListSessions(h.ctx, "i1"); err != nil || len(sessions) != 0 {
				t.Fatalf("核对没过不应留下会话：%+v err=%v", sessions, err)
			}
			h.trade.checkErr = nil
			recovered := h.setEnabled("i1", true)
			requireOK(t, int32(recovered.GetRetInfo().GetCode()), recovered.GetRetInfo().GetMsg())
			if !recovered.GetInstance().GetEnabled() || len(h.trade.closes) != 0 {
				t.Fatalf("重试应启用，且不联系 Trade 关闭任何会话：%+v closes=%v", recovered.GetInstance(), h.trade.closes)
			}
		})
	}
}

// 停用时 Trade 没确认关闭：实例已经停用、会话已关闭，提示再点一次停用；再点停用对同一个会话、同一个账户重试，不做后台重试。
func TestDisableReportsUnconfirmedTradeClose(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true).GetInstance()
	h.trade.closeErr = errors.New("Trade 账户关闭会话失败：Trade 或网关不可达，结果未知")
	rsp := h.setEnabled("i1", false)
	for _, want := range []string{"实例已停用", "Trade 未确认", "再点一次停用", "暂停组合账户 acct-1"} {
		requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), want)
	}
	if rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" {
		t.Fatalf("Trade 没确认时实例也应已停用、没有会话：%+v", rsp.GetInstance())
	}
	if session, err := h.service.Store.GetSession(h.ctx, first.GetSessionId()); err != nil || session.ClosedAt == nil {
		t.Fatalf("会话应已关闭：%+v err=%v", session, err)
	}
	retried := h.setEnabled("i1", false)
	requireFail(t, int32(retried.GetRetInfo().GetCode()), retried.GetRetInfo().GetMsg(), "Trade 未确认")
	h.trade.closeErr = nil
	done := h.setEnabled("i1", false)
	requireOK(t, int32(done.GetRetInfo().GetCode()), done.GetRetInfo().GetMsg())
	if len(h.trade.closes) != 3 {
		t.Fatalf("每次停用调一次 Trade：%v", h.trade.closes)
	}
	for _, call := range h.trade.closes {
		if call.account != "acct-1" || call.session != first.GetSessionId() || call.instance != "i1" || call.startedAt <= 0 {
			t.Fatalf("重试应对同一个会话、同一个账户：%v", h.trade.closes)
		}
	}
}

// 用例“停用后改账户，再点停用仍按原账户调用”：关闭按会话里记的账户调，不受之后改账户（含改成观察实例）影响。
func TestDisableAfterAccountChangeStillClosesOriginalAccount(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true).GetInstance()
	h.trade.closeErr = errors.New("trade unavailable")
	requireFail(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "", "")
	h.trade.closeErr = nil
	update := func(account string) {
		t.Helper()
		rsp, err := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", LogicalAccountId: account}})
		if err != nil {
			t.Fatal(err)
		}
		requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	}
	// 停用后立刻能改实例（没有握手没完成的中间状态）：改到别的账户，再点停用仍调原账户。
	update("acct-2")
	h.trade.closes = nil
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	if len(h.trade.closes) != 1 || h.trade.closes[0].account != "acct-1" || h.trade.closes[0].session != first.GetSessionId() {
		t.Fatalf("改账户后再点停用应仍按原账户调用：%v", h.trade.closes)
	}
	// 改成观察实例并启用、停用一次：最近一次会话没挂账户，再点停用取更早挂过账户的那次。
	update("")
	observer := h.setEnabled("i1", true).GetInstance().GetSessionId()
	h.trade.closes = nil
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	if len(h.trade.closes) != 0 {
		t.Fatalf("观察会话停用不联系 Trade：%v", h.trade.closes)
	}
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	if len(h.trade.closes) != 1 || h.trade.closes[0].account != "acct-1" || h.trade.closes[0].session != first.GetSessionId() || observer == first.GetSessionId() {
		t.Fatalf("最近一次会话没挂账户时应取更早挂过账户的那次：%v", h.trade.closes)
	}
	// 改到新账户并重新启用之后，最近一次挂账户的会话是新账户的：旧账户那次关闭不再有入口（写进文档的已知情况）。
	update("acct-2")
	second := h.setEnabled("i1", true).GetInstance().GetSessionId()
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	h.trade.closes = nil
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	if len(h.trade.closes) != 1 || h.trade.closes[0].account != "acct-2" || h.trade.closes[0].session != second {
		t.Fatalf("重新启用之后再点停用对新账户的会话：%v", h.trade.closes)
	}
}

// 观察实例（没挂账户）的启用、停用、重复停用都不联系 Trade。
func TestObserverInstanceNeverCallsTrade(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i1", "s1", "ftask_i1", "", true).GetRetInfo().GetCode()), "")
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	if len(h.trade.checks) != 0 || len(h.trade.closes) != 0 {
		t.Fatalf("观察实例不应联系 Trade：checks=%v closes=%v", h.trade.checks, h.trade.closes)
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
	rsp := h.createInstance("i1", "s1", "ftask_i1", "", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "leverage")
	if rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" {
		t.Fatalf("解析失败不应留下会话：%+v", rsp.GetInstance())
	}
}

func TestCreateInstanceRetrySemantics(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "ftask_i1", "", false)
	requireOK(t, int32(first.GetRetInfo().GetCode()), first.GetRetInfo().GetMsg())
	same := h.createInstance("i1", "s1", "ftask_i1", "", false)
	requireOK(t, int32(same.GetRetInfo().GetCode()), same.GetRetInfo().GetMsg())
	conflict := h.createInstance("i1", "s1", "ftask_other", "", false)
	requireFail(t, int32(conflict.GetRetInfo().GetCode()), conflict.GetRetInfo().GetMsg(), "冲突")
	enable := h.createInstance("i1", "s1", "ftask_i1", "", true)
	requireFail(t, int32(enable.GetRetInfo().GetCode()), enable.GetRetInfo().GetMsg(), "SetStrategyInstanceEnabled")
	missing := h.createInstance("i2", "nope", "ftask_i2", "", false)
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "不存在")
	other := trpc.BackgroundContext()
	trpc.SetMetaData(other, "x-space-id", []byte("other"))
	scoped, _ := h.service.GetStrategyInstance(other, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(scoped.GetRetInfo().GetCode()), scoped.GetRetInfo().GetMsg(), "空间")
}

// S20：停用、修改定义、重新启用后，旧会话结果仍按旧 DSL 与旧绑定查看；被实例引用的定义不能删除。
func TestS20HistoryAfterRedefinition(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "", true)
	oldSession := created.GetInstance().GetSessionId()
	result := store.Result{ResultID: "r-old", InstanceID: "i1", SessionID: oldSession, BarEndTime: h.now.Add(time.Hour), ValidUntil: h.now.Add(3 * time.Hour), Status: store.StatusOK, DSLHash: dsl.Hash([]byte(demoDSL)), InputJSON: []byte(`{"views":{"dataset_factor_i1":{}}}`), TargetsJSON: []byte(`[{"instrument_id":"BTC-USDT","target_weight":"1"}]`), SummaryJSON: []byte(`{}`), PublishStatus: store.PublishNone, CreatedAt: h.now}
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
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	got, _ := h.service.GetStrategyResult(h.ctx, &strategypb.GetStrategyResultReq{ResultId: "r-old"})
	requireOK(t, int32(got.GetRetInfo().GetCode()), got.GetRetInfo().GetMsg())
	if got.GetDslYaml() != demoDSL || !strings.Contains(got.GetResolvedJson(), `"dataset_id":"dataset_factor_i1"`) || len(got.GetItems()) != 1 || got.GetItems()[0].GetWeight() != "1" {
		t.Fatalf("旧会话结果应按旧 DSL 与旧绑定展示：dsl=%q resolved=%s items=%+v", got.GetDslYaml(), got.GetResolvedJson(), got.GetItems())
	}
	targets, _ := h.service.ListStrategyTargets(h.ctx, &strategypb.ListStrategyTargetsReq{InstanceId: "i1"})
	if len(targets.GetTargets()) != 0 || targets.GetSessionId() != reenabled.GetInstance().GetSessionId() {
		t.Fatalf("新会话不应返回旧会话的目标：%+v", targets)
	}
	// 被实例引用的定义不能删除（实例永久存在）。
	refused, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: "s1"})
	requireFail(t, int32(refused.GetRetInfo().GetCode()), refused.GetRetInfo().GetMsg(), "引用")
	history, _ := h.service.ListStrategyResults(h.ctx, &strategypb.ListStrategyResultsReq{InstanceId: "i1"})
	requireOK(t, int32(history.GetRetInfo().GetCode()), history.GetRetInfo().GetMsg())
	if history.GetTotal() != 1 || history.GetResults()[0].GetResultId() != "r-old" {
		t.Fatalf("旧会话的历史结果仍应可查：%+v", history)
	}
}

// 验收项 4：ValidateStrategy 对引用不存在列的 DSL 返回明确的列名错误；有数据时返回试算结果与解释。
func TestValidateStrategy(t *testing.T) {
	h := newHarness(t)
	plain, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL})
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	h.resolver.err = errors.New("列 momentum_99 不存在于 View view_a；可用列：close")
	missing, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, FactorTasks: map[string]string{"spot": "ftask_a"}})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "momentum_99")
	h.resolver.err = nil
	frame := engine.Frame{BarEnd: h.now, Markets: map[string]engine.MarketInput{"spot": {Rows: map[string]engine.Row{"BTC-USDT": {Values: map[string]float64{"close": 2}}, "ETH-USDT": {Values: map[string]float64{"close": 1}}}}}, Universe: []string{"BTC-USDT", "ETH-USDT"}, Expected: map[string][]string{"r": {"BTC-USDT", "ETH-USDT"}}, AgedOut: map[string][]string{}}
	h.resolver.loaded = input.Loaded{Frame: frame, Boundary: input.PeriodBoundaries{BarEnd: h.now, StorageStart: h.now.Add(-time.Hour)}}
	trial, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, FactorTasks: map[string]string{"spot": "ftask_a"}})
	requireOK(t, int32(trial.GetRetInfo().GetCode()), trial.GetRetInfo().GetMsg())
	if trial.GetTrial().GetStatus() != "ok" || len(trial.GetTrial().GetTargets()) != 1 || trial.GetTrial().GetTargets()[0].GetInstrumentId() != "BTC-USDT" || len(trial.GetTrialItems()) != 2 || trial.GetResolvedJson() == "" {
		t.Fatalf("试算结果不符：%+v", trial)
	}
	h.resolver.loadErr = fmt.Errorf("View view_a 最近三个周期都没有数据")
	empty, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, FactorTasks: map[string]string{"spot": "ftask_a"}})
	requireOK(t, int32(empty.GetRetInfo().GetCode()), empty.GetRetInfo().GetMsg())
	if len(empty.GetDiagnostics()) == 0 || !strings.Contains(empty.GetDiagnostics()[0], "试算未完成") {
		t.Fatalf("没有数据时应给出诊断：%+v", empty.GetDiagnostics())
	}
}

// S26：登记的市场必须与任务的源数据集一致（把永续任务填在现货下被拒绝）；现货回放可以发起、查询、取消。
func TestS26StartReplayChecksTaskMarkets(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	swap, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_swap"}, StartTime: ms("2026-09-01T00:00:00Z"), EndTime: ms("2026-09-02T00:00:00Z")})
	requireFail(t, int32(swap.GetRetInfo().GetCode()), swap.GetRetInfo().GetMsg(), "登记为 spot 市场")
	empty, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", StartTime: ms("2026-09-01T00:00:00Z"), EndTime: ms("2026-09-02T00:00:00Z")})
	requireFail(t, int32(empty.GetRetInfo().GetCode()), empty.GetRetInfo().GetMsg(), "factor_tasks 不能为空")
	badRange, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-02T00:00:00Z"), EndTime: ms("2026-09-01T00:00:00Z")})
	requireFail(t, int32(badRange.GetRetInfo().GetCode()), badRange.GetRetInfo().GetMsg(), "晚于")
	noStart, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, EndTime: ms("2026-09-02T00:00:00Z")})
	requireFail(t, int32(noStart.GetRetInfo().GetCode()), noStart.GetRetInfo().GetMsg(), "start_time 必须是正的 Unix 毫秒时刻")
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SpotFeeBps: 10})
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
	trpc.SetMetaData(other, "x-space-id", []byte("other"))
	foreign, _ := h.service.GetReplay(other, &strategypb.GetReplayReq{ReplayId: started.GetReplay().GetReplayId()})
	requireFail(t, int32(foreign.GetRetInfo().GetCode()), foreign.GetRetInfo().GetMsg(), "空间")
}

// 停用时 Trade 没确认关闭，随后再启用：不被挡住，新建会话并重新解析绑定，新会话的开始时刻更晚（Trade 按它覆盖旧会话的目标）。
func TestReenableAfterUnconfirmedCloseCreatesNewSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	first := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true).GetInstance()
	h.trade.closeErr = errors.New("trade unavailable")
	disabled := h.setEnabled("i1", false)
	requireFail(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg(), "Trade 未确认")
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if reenabled.GetInstance().GetSessionId() == first.GetSessionId() || h.resolver.calls != 2 {
		t.Fatalf("应新建会话并重新解析绑定：%+v calls=%d", reenabled.GetInstance(), h.resolver.calls)
	}
	previous, _ := h.service.Store.GetSession(h.ctx, first.GetSessionId())
	next, _ := h.service.Store.GetSession(h.ctx, reenabled.GetInstance().GetSessionId())
	if next.StartedAt <= previous.StartedAt || len(h.trade.closes) != 1 {
		t.Fatalf("新会话开始更晚，启用不再调关闭：%d <= %d closes=%v", next.StartedAt, previous.StartedAt, h.trade.closes)
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
	both, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", DslYaml: demoDSL, FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02")})
	requireFail(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg(), "只能给出其一")
	neither, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02")})
	requireFail(t, int32(neither.GetRetInfo().GetCode()), neither.GetRetInfo().GetMsg(), "只能给出其一")
	h.resolver.view = input.ViewInfo{DatasetID: "dataset_factor_a", ActiveIndexID: "idx", Generation: "idx@b1", IndexedFrom: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), IndexedTo: time.Date(2026, 9, 3, 5, 0, 0, 0, time.UTC)}
	early, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02")})
	requireFail(t, int32(early.GetRetInfo().GetCode()), early.GetRetInfo().GetMsg(), "可用起点")
	future, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-02"), EndTime: ms("2026-09-30")})
	requireOK(t, int32(future.GetRetInfo().GetCode()), future.GetRetInfo().GetMsg())
	// 最新一根的 bar_start 是 09-03 05:00：它可能还没写完，终点截到它的 bar_start，回放到它之前一根为止。
	if end := future.GetReplay().GetEndTime(); end != ms("2026-09-03T05:00:00Z") {
		t.Fatalf("终点应截到最新一根的 bar_start：%d", end)
	}
	if future.GetBarCount() != 29 || future.GetFirstBarEnd() != ms("2026-09-02T01:00:00Z") || future.GetLastBarEnd() != ms("2026-09-03T05:00:00Z") {
		t.Fatalf("应返回实际回放的根数与首末根：%d %d %d", future.GetBarCount(), future.GetFirstBarEnd(), future.GetLastBarEnd())
	}
	stored, err := h.service.Store.GetReplay(h.ctx, future.GetReplay().GetReplayId())
	if err != nil {
		t.Fatal(err)
	}
	bars, err := input.ReplayBars(input.DefaultCalendar, "1h", stored.StartTime, stored.EndTime, input.DefaultReplayMaxBars)
	if err != nil || len(bars) != 29 {
		t.Fatalf("落库往返后仍应是 29 根：%d err=%v", len(bars), err)
	}
	badFee, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-02"), EndTime: ms("2026-09-03"), SpotFeeBps: 1500})
	requireFail(t, int32(badFee.GetRetInfo().GetCode()), badFee.GetRetInfo().GetMsg(), "fee_bps")
	h.resolver.view.IndexedFrom = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	tooLong, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2023-01-02"), EndTime: ms("2026-09-30")})
	requireFail(t, int32(tooLong.GetRetInfo().GetCode()), tooLong.GetRetInfo().GetMsg(), "20000")
}

// 同一空间的组合账户已被另一个启用实例绑定时，启用在联系 Trade 之前就被拒绝。
func TestEnableRejectsAccountHeldByAnotherInstance(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "ftask_i1", "acct-1", true)
	checks := len(h.trade.checks)
	second := h.createInstance("i2", "s1", "ftask_i2", "acct-1", true)
	requireFail(t, int32(second.GetRetInfo().GetCode()), second.GetRetInfo().GetMsg(), "已被启用实例 i1 绑定")
	if len(h.trade.checks) != checks {
		t.Fatalf("不应联系 Trade：%v", h.trade.checks)
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

// 会话或 DSL 版本快照读取出错（不是不存在）时，GetStrategyResult 失败而不是以成功返回空快照；页面会缓存成功的响应。
func TestGetStrategyResultFailsWhenSnapshotReadErrors(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "", true)
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: created.GetInstance().GetSessionId(), BarEndTime: h.now.Add(time.Hour), ValidUntil: h.now.Add(3 * time.Hour), Status: store.StatusOK, DSLHash: dsl.Hash([]byte(demoDSL)), InputJSON: []byte(`{}`), TargetsJSON: []byte(`[]`), SummaryJSON: []byte(`{}`), PublishStatus: store.PublishNone, CreatedAt: h.now}
	if _, _, err := h.service.Store.CommitResult(h.ctx, store.CommitRequest{Result: result, Now: h.now}); err != nil {
		t.Fatal(err)
	}
	ok, _ := h.service.GetStrategyResult(h.ctx, &strategypb.GetStrategyResultReq{ResultId: "r1"})
	requireOK(t, int32(ok.GetRetInfo().GetCode()), ok.GetRetInfo().GetMsg())
	// 版本表读取出错（这里改掉列名模拟数据库读错误）：整体失败，不返回空 DSL 的成功响应。
	// ApplySchema 在改动提交之后才校验 schema，会报“旧 schema”；这里要的正是这个被改坏的库，忽略它的错误。
	_ = h.service.Store.ApplySchema(`ALTER TABLE t_strategy_def_versions RENAME COLUMN c_dsl_yaml TO c_dsl_yaml_broken;`)
	failed, _ := h.service.GetStrategyResult(h.ctx, &strategypb.GetStrategyResultReq{ResultId: "r1"})
	if failed.GetRetInfo().GetCode() == 0 {
		t.Fatalf("快照读取出错应失败：%+v", failed.GetRetInfo())
	}
}

// 研究回放支持永续与两个市场的策略：回放记录按市场保存任务、View 与索引代次，费率分现货与永续两个。
func TestStartReplaySupportsSwapAndDualTasks(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	swap, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_swap"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SwapFeeBps: 5})
	requireOK(t, int32(swap.GetRetInfo().GetCode()), swap.GetRetInfo().GetMsg())
	if tasks := swap.GetReplay().GetTasks(); len(tasks) != 1 || tasks["swap"].GetFactorTaskId() != "ftask_swap" || swap.GetReplay().GetSwapFeeBps() != 5 {
		t.Fatalf("永续回放的任务与费率不符：%+v", swap.GetReplay())
	}
	both, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a", "swap": "ftask_swap"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SpotFeeBps: 10, SwapFeeBps: 5})
	requireOK(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg())
	stored, err := h.service.Store.GetReplay(h.ctx, both.GetReplay().GetReplayId())
	if err != nil || len(stored.Tasks) != 2 || stored.Tasks["spot"].FactorTaskID != "ftask_a" || stored.Tasks["swap"].FactorTaskID != "ftask_swap" || stored.Tasks["swap"].ViewGeneration == "" || stored.SpotFeeBps != 10 || stored.SwapFeeBps != 5 {
		t.Fatalf("双任务回放的记录不符：%+v err=%v", stored, err)
	}
	badFee, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_swap"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SwapFeeBps: 1500})
	requireFail(t, int32(badFee.GetRetInfo().GetCode()), badFee.GetRetInfo().GetMsg(), "swap_fee_bps")
	// 滑点与手续费同一范围，随回放记录保存并在详情里返回。
	slipped, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_swap"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SlippageBps: 3})
	requireOK(t, int32(slipped.GetRetInfo().GetCode()), slipped.GetRetInfo().GetMsg())
	if slipped.GetReplay().GetSlippageBps() != 3 {
		t.Fatalf("回放应带上滑点：%+v", slipped.GetReplay())
	}
	for _, value := range []float64{-1, 201} {
		badSlippage, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_swap"}, StartTime: ms("2026-09-01"), EndTime: ms("2026-09-02"), SlippageBps: value})
		requireFail(t, int32(badSlippage.GetRetInfo().GetCode()), badSlippage.GetRetInfo().GetMsg(), "slippage_bps")
	}
}

// 停用后把观察实例改挂组合账户再启用：规则状态在修改实例时清空，新账户不接手原来积累的理论状态；不改实例直接再启用则沿用。
func TestReenableAfterAccountChangeStartsWithoutRuleState(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	first := created.GetInstance().GetSessionId()
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: first, BarEndTime: h.now.Add(time.Hour), ValidUntil: h.now.Add(3 * time.Hour), Status: store.StatusOK, DSLHash: dsl.Hash([]byte(demoDSL)), InputJSON: []byte(`{}`), TargetsJSON: []byte(`[]`), SummaryJSON: []byte(`{}`), PublishStatus: store.PublishNone, CreatedAt: h.now}
	if _, _, err := h.service.Store.CommitResult(h.ctx, store.CommitRequest{Result: result, State: []byte(`{"rules":{"r":{"held":["BTC-USDT"]}}}`), Now: h.now}); err != nil {
		t.Fatal(err)
	}
	// 不改实例：停用再启用沿用状态。
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "停用")
	kept := h.setEnabled("i1", true)
	requireOK(t, int32(kept.GetRetInfo().GetCode()), kept.GetRetInfo().GetMsg())
	if state, found, err := h.service.Store.GetState(h.ctx, "i1"); err != nil || !found || state.SessionID != kept.GetInstance().GetSessionId() || state.SessionID == first {
		t.Fatalf("没改实例时状态应挂到新会话沿用：%+v found=%v err=%v", state, found, err)
	}
	// 改挂账户：状态清空。
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "停用")
	updated, err := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", LogicalAccountId: "acct-new"}})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(updated.GetRetInfo().GetCode()), updated.GetRetInfo().GetMsg())
	again := h.setEnabled("i1", true)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetLogicalAccountId() != "acct-new" {
		t.Fatalf("账户应已改为新账户：%+v", again.GetInstance())
	}
	if state, found, err := h.service.Store.GetState(h.ctx, "i1"); err != nil || found {
		t.Fatalf("改了账户后不应沿用规则状态：%+v found=%v err=%v", state, found, err)
	}
}
