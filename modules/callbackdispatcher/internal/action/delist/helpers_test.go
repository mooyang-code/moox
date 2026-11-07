package delist

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/stretchr/testify/require"
)

var (
	baseTime = time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	foo      = action.InstrumentRef{Exchange: "binance", Market: "SPOT", ExchangeSymbol: "FOOUSDT"}
)

// clock 是测试用的模拟时钟。
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// fakeTrade 是假的 Trade：名单、持仓和清仓操作都在内存里，清仓按操作号幂等。
type fakeTrade struct {
	mu sync.Mutex
	// blocked 是下架名单：标的 → 第一个写入者的原因（重复写入保留第一个，和 Trade 一样）。
	blocked map[action.InstrumentRef]string
	holders []action.Holder
	// ignorePaperOnly 模拟违约的 Trade：paperOnly 为真也返回全部持有者。
	ignorePaperOnly bool
	unknown         bool
	// skipped 和 notes 是 ListInstrumentHolders 带回的没能判断的账户与说明。
	skipped []action.SkippedAccount
	notes   []string

	// 各接口的钩子与注入的错误。
	blockHook   func(ctx context.Context)
	unblockHook func(ctx context.Context) error
	errs        map[string]error
	busyLeft    map[string]int
	// finalFor 决定一次清仓最终的状态，默认 COMPLETED；key 是 空间/组合。
	finalFor func(request action.FlattenRequest) (status, message string)

	calls    map[string]int
	flattens []action.FlattenRequest
	ops      map[string]*fakeOp
}

type fakeOp struct {
	request action.FlattenRequest
	status  string
	message string
	polls   int
}

func newFakeTrade() *fakeTrade {
	return &fakeTrade{
		blocked: map[action.InstrumentRef]string{}, errs: map[string]error{}, busyLeft: map[string]int{},
		calls: map[string]int{}, ops: map[string]*fakeOp{},
	}
}

func (f *fakeTrade) setHolders(holders ...action.Holder) {
	f.mu.Lock()
	f.holders = holders
	f.mu.Unlock()
}

func (f *fakeTrade) isBlocked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, blocked := f.blocked[foo]
	return blocked
}

// setBlocked 直接改名单（不经过处理器）：写入的原因是“手工”，不带任何执行记录的标记。
func (f *fakeTrade) setBlocked(blocked bool) {
	f.mu.Lock()
	if blocked {
		f.blocked[foo] = "手工"
	} else {
		delete(f.blocked, foo)
	}
	f.mu.Unlock()
}

func (f *fakeTrade) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeTrade) flattenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.flattens)
}

func (f *fakeTrade) setErr(method string, err error) {
	f.mu.Lock()
	f.errs[method] = err
	f.mu.Unlock()
}

func (f *fakeTrade) enter(method string) error {
	f.calls[method]++
	return f.errs[method]
}

func (f *fakeTrade) BlockInstrument(ctx context.Context, instrument action.InstrumentRef, reason, eventID string) (action.BlockResult, error) {
	f.mu.Lock()
	hook := f.blockHook
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Block"); err != nil {
		return action.BlockResult{}, err
	}
	_, exists := f.blocked[instrument]
	if !exists {
		f.blocked[instrument] = reason
	}
	created := !exists
	return action.BlockResult{InstrumentUnknown: f.unknown, Created: created}, nil
}

func (f *fakeTrade) UnblockInstrument(ctx context.Context, instrument action.InstrumentRef) error {
	f.mu.Lock()
	hook := f.unblockHook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Unblock"); err != nil {
		return err
	}
	delete(f.blocked, instrument)
	return nil
}

func (f *fakeTrade) ListBlockedInstruments(context.Context) ([]action.BlockedInstrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListBlocked"); err != nil {
		return nil, err
	}
	var list []action.BlockedInstrument
	for instrument, reason := range f.blocked {
		list = append(list, action.BlockedInstrument{Instrument: instrument, Reason: reason})
	}
	return list, nil
}

func (f *fakeTrade) ListInstrumentHolders(_ context.Context, _ action.InstrumentRef, paperOnly bool) (action.HoldersResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListHolders"); err != nil {
		return action.HoldersResult{}, err
	}
	var result []action.Holder
	for _, holder := range f.holders {
		if paperOnly && !holder.Paper && !f.ignorePaperOnly {
			continue
		}
		result = append(result, holder)
	}
	return action.HoldersResult{Holders: result, Skipped: f.skipped, Notes: f.notes}, nil
}

func accountKey(spaceID, logicalAccountID string) string { return spaceID + "/" + logicalAccountID }

func (f *fakeTrade) FlattenInstrument(_ context.Context, request action.FlattenRequest) (action.OperatorAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Flatten"); err != nil {
		return action.OperatorAction{}, err
	}
	key := accountKey(request.SpaceID, request.LogicalAccountID)
	if op, exists := f.ops[request.ActionID]; exists {
		// 和 Trade 一样：同一个操作号的请求内容（含截止时间、说明）必须一致，否则判幂等冲突。
		if op.request.DeadlineAt != request.DeadlineAt || op.request.Reason != request.Reason {
			return action.OperatorAction{}, errors.New("Trade 返回 CONFLICT：同一个操作号的参数不一致")
		}
		// 同一个操作号幂等：不重复平仓，返回当前状态；FAILED 的用同一个号再调会重新执行。
		if op.status == action.OperatorFailed {
			op.status = action.OperatorRunning
		}
		return action.OperatorAction{ActionID: request.ActionID, Status: op.status, Message: op.message}, nil
	}
	if f.busyLeft[key] > 0 {
		f.busyLeft[key]--
		return action.OperatorAction{}, action.ErrTradeBusy
	}
	f.flattens = append(f.flattens, request)
	op := &fakeOp{request: request, status: action.OperatorRunning}
	f.ops[request.ActionID] = op
	return action.OperatorAction{ActionID: request.ActionID, Status: op.status}, nil
}

func (f *fakeTrade) GetOperatorAction(_ context.Context, spaceID, operatorActionID string) (action.OperatorAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetAction"); err != nil {
		return action.OperatorAction{}, err
	}
	op, ok := f.ops[operatorActionID]
	if !ok || op.request.SpaceID != spaceID {
		return action.OperatorAction{}, errors.New("操作不存在")
	}
	if op.status == action.OperatorRunning {
		op.polls++
		status, message := action.OperatorCompleted, ""
		if f.finalFor != nil {
			status, message = f.finalFor(op.request)
		}
		op.status, op.message = status, message
		if status == action.OperatorCompleted {
			f.removeHolder(op.request.SpaceID, op.request.LogicalAccountID)
		}
	}
	return action.OperatorAction{ActionID: operatorActionID, Status: op.status, Message: op.message}, nil
}

func (f *fakeTrade) removeHolder(spaceID, logicalAccountID string) {
	kept := f.holders[:0:0]
	for _, holder := range f.holders {
		if holder.SpaceID == spaceID && holder.LogicalAccountID == logicalAccountID && !holder.Dust {
			continue
		}
		kept = append(kept, holder)
	}
	f.holders = kept
}

func live(spaceID, logicalAccountID string) action.Holder {
	return action.Holder{SpaceID: spaceID, LogicalAccountID: logicalAccountID, Exchange: "binance", Quantity: "10"}
}

func paper(spaceID, logicalAccountID string) action.Holder {
	return action.Holder{SpaceID: spaceID, LogicalAccountID: logicalAccountID, Exchange: "binance", Quantity: "5", Paper: true}
}

func payloadAt(delistAt time.Time, scope string) string {
	return fmt.Sprintf(`{"exchange":"binance","market":"SPOT","exchange_symbol":"FOOUSDT","delist_at":%q,"source":"MANUAL","scope":%q}`,
		delistAt.UTC().Format(time.RFC3339), scope)
}

// harness 把真实的临时 SQLite、假 Trade 和模拟时钟装在一起。
type harness struct {
	t       *testing.T
	store   *store.Store
	trade   *fakeTrade
	clock   *clock
	handler *Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dbStore, err := store.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	require.NoError(t, err)
	require.NoError(t, dbStore.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = dbStore.Close() })
	require.NoError(t, dbStore.EnsureSystemRule(context.Background(), store.Rule{
		RuleID: "sys-1", Name: "标的即将下架时卖出策略持有的这个币", EventName: "event.collector.instrument.delist_scheduled",
		ActionType: store.ActionSystem, SystemAction: store.SystemActionSellDelistedHoldings, System: true, Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}))
	trade := newFakeTrade()
	clk := &clock{t: baseTime}
	return &harness{t: t, store: dbStore, trade: trade, clock: clk, handler: &Handler{Store: dbStore, Trade: trade, Now: clk.Now}}
}

// addExecution 新建一条下架执行记录（直接写库，等价于事件接收的结果）。
func (h *harness) addExecution(id string, delistAt time.Time, scope string) store.Execution {
	h.t.Helper()
	execution := store.Execution{
		ExecutionID: id, RuleID: "sys-1", RuleName: "规则", ActionType: store.ActionSystem, EventID: "event-" + id,
		EventName: "event.collector.instrument.delist_scheduled", SpaceID: "mooxsys", SubjectID: foo.Exchange + ":" + foo.Market + ":" + foo.ExchangeSymbol,
		OccurredAt: baseTime.UnixMilli(), PayloadJSON: payloadAt(delistAt, scope), Status: store.StatusPending,
		DedupeKey: "sys-1|binance:SPOT:FOOUSDT|" + scope + "|" + id, NextRunAt: baseTime.UnixMilli(), CreatedAt: 1, UpdatedAt: 1,
	}
	require.NoError(h.t, h.store.InTx(context.Background(), func(tx *store.Tx) error { return tx.InsertExecution(execution) }))
	return execution
}

// step 把时钟拨到 at，直接推进一步。
func (h *harness) step(id string, at time.Time) action.Outcome {
	h.t.Helper()
	h.clock.Set(at)
	execution, err := h.store.GetExecution(context.Background(), id)
	require.NoError(h.t, err)
	outcome, err := h.handler.Step(context.Background(), execution, at)
	require.NoError(h.t, err)
	return outcome
}

func (h *harness) steps(id string) map[stepKey]store.DelistAction {
	h.t.Helper()
	list, err := h.store.ListDelistActions(context.Background(), id)
	require.NoError(h.t, err)
	result := map[stepKey]store.DelistAction{}
	for _, item := range list {
		result[keyOf(item)] = item
	}
	return result
}

func flattenKey(round int, spaceID, logicalAccountID string) stepKey {
	return stepKey{Round: round, Kind: store.DelistKindFlatten, SpaceID: spaceID, LogicalAccountID: logicalAccountID}
}

func summaryKey(round int) stepKey { return stepKey{Round: round, Kind: store.DelistKindFlatten} }

func blockKey() stepKey { return stepKey{Round: RoundBlock, Kind: store.DelistKindBlock} }

func verifyKey() stepKey { return stepKey{Round: RoundVerify, Kind: store.DelistKindVerify} }
