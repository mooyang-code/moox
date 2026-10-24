package seriesreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type fakeFactor struct {
	mu       sync.Mutex
	opens    int
	steps    []string
	writes   []string
	failOnce map[string]error
	gone     bool
	// params 记录每期（不含写回）请求带来的参数快照。
	params map[string]MemberParams
	// applied 非空时模拟引擎的参数校验：返回这一期实际生效的参数与被拒绝的成员。
	applied func(req FactorStepRequest) (MemberParams, []ParamRejection)
}

func (f *fakeFactor) Open(_ context.Context, req FactorOpenRequest) (FactorOpenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	f.gone = false
	return FactorOpenResponse{ReplayID: req.ReplayID, Subjects: []string{"A"}, StepSecs: 60, Factors: []FactorInfo{{Member: "f", FactorID: "f", DefinitionHash: "h1"}}}, nil
}

func (f *fakeFactor) Step(_ context.Context, req FactorStepRequest) (FactorStepResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone {
		return FactorStepResponse{}, &HTTPError{Module: "因子引擎", Status: http.StatusNotFound, Text: "会话不存在"}
	}
	if err := f.failOnce[req.Period]; err != nil {
		delete(f.failOnce, req.Period)
		return FactorStepResponse{}, err
	}
	if req.WriteFactors {
		f.writes = append(f.writes, req.Period)
	} else {
		f.steps = append(f.steps, req.Period)
		if f.params == nil {
			f.params = map[string]MemberParams{}
		}
		f.params[req.Period] = req.Params
	}
	rsp := FactorStepResponse{Period: req.Period, Subjects: map[string]FactorSubject{"A": {Close: 100, Exec: 101, Factors: map[string]map[string]float64{"f": {"m": 1}}}}, States: map[string]FactorState{"f": {Status: "complete", DefinitionHash: "h1"}}}
	if f.applied != nil {
		rsp.AppliedParams, rsp.RejectedParams = f.applied(req)
	}
	return rsp, nil
}

func (f *fakeFactor) Close(context.Context, string) error { return nil }

type fakeStrategy struct {
	mu     sync.Mutex
	states []string
	hash   string
	// next 非空时，按周期产出下一期的因子参数（返回 nil 表示这一期不产出）。
	next func(period time.Time) MemberParams
	// setID 非空时，绑定带实例的私有因子集。
	setID string
}

func (s *fakeStrategy) Bind(context.Context, string) (StrategyBinding, error) {
	hash := s.hash
	if hash == "" {
		hash = "dsl1"
	}
	return StrategyBinding{InstanceID: "i1", DSLHash: hash, Factors: map[string]string{"f": "h1"}, PreviousDepth: 1, SourceColumns: []string{"close"}, FactorSetID: s.setID, Tuned: s.next != nil}, nil
}

// 决策：状态里记录已处理的周期数；偶数周期 ok、奇数周期跳过。
func (s *fakeStrategy) Evaluate(_ context.Context, req StrategyEvaluateRequest) (StrategyDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, string(req.State))
	count := 0
	if len(req.State) > 0 {
		var st struct{ N int }
		_ = json.Unmarshal(req.State, &st)
		count = st.N
	}
	period, _ := time.Parse(time.RFC3339, req.Period)
	decision := StrategyDecision{Status: "ok", Targets: []StrategyTarget{{InstrumentID: "A", TargetWeight: "0.5"}}, State: json.RawMessage(fmt.Sprintf(`{"N":%d}`, count+1))}
	if period.Minute()%2 == 1 {
		decision.Status, decision.SkipReason, decision.Targets = "skipped", "factor_skipped", nil
		decision.State = req.State
	}
	if s.next != nil && decision.Status == "ok" {
		decision.NextParams = s.next(period)
	}
	return decision, nil
}

type fakeTrade struct {
	mu       sync.Mutex
	sessions map[string]bool
	advances []TradeAdvanceRequest
	failNext error
	closed   bool
}

func (t *fakeTrade) CreateSession(_ context.Context, req TradeCreateRequest) (TradeSession, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[string]bool{}
	}
	t.sessions[req.ReplayID] = true
	return TradeSession{ReplayID: req.ReplayID, InitialCash: "100000"}, nil
}

func (t *fakeTrade) GetSession(context.Context, string) (TradeSession, error) {
	return TradeSession{}, nil
}

func (t *fakeTrade) Advance(_ context.Context, req TradeAdvanceRequest) (TradeResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failNext != nil {
		err := t.failNext
		t.failNext = nil
		return TradeResult{}, err
	}
	for _, a := range t.advances {
		if a.Period == req.Period {
			return TradeResult{Replayed: true}, nil
		}
	}
	t.advances = append(t.advances, req)
	return TradeResult{ReplayID: req.ReplayID, Period: req.Period}, nil
}

func (t *fakeTrade) Periods(context.Context, string, int64, int) ([]TradeResult, error) {
	return nil, nil
}
func (t *fakeTrade) Close(context.Context, string) error  { t.closed = true; return nil }
func (t *fakeTrade) Delete(context.Context, string) error { return nil }

func newRunner(t *testing.T) (*Runner, *fakeFactor, *fakeStrategy, *fakeTrade) {
	t.Helper()
	store, err := OpenStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	f, s, tr := &fakeFactor{failOnce: map[string]error{}}, &fakeStrategy{}, &fakeTrade{}
	return &Runner{Store: store, Factor: f, Strategy: s, Trade: tr, PollInterval: 5 * time.Millisecond}, f, s, tr
}

func spec(minutes int) Spec {
	return Spec{ReplayID: "r1", SetID: "set", InstanceID: "i1", From: t0.Format(time.RFC3339), To: t0.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339), WriteFactors: true}
}

// 逐期推进：每期取因子帧、求值、记录、向 Trade 推进、写回因子；策略状态逐期传递，跳过的周期不改状态。
func TestRunAdvancesEveryPeriodInOrder(t *testing.T) {
	r, f, s, tr := newRunner(t)
	run, err := r.Run(context.Background(), spec(4))
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, run.Status)
	require.Equal(t, 4, run.PeriodsDone)
	require.Equal(t, 4, run.PeriodsTotal)
	require.Len(t, f.steps, 4)
	require.Len(t, f.writes, 4, "write_factors 时每期写回一次")
	require.Len(t, tr.advances, 4)
	require.True(t, tr.closed)
	// 决策时刻 = K 线起点 + 周期（毫秒）；报价带估值价和成交价。
	require.Equal(t, t0.Add(time.Minute).UnixMilli(), tr.advances[0].Period)
	require.Equal(t, "UPDATE_TARGET", tr.advances[0].Decision.Kind)
	require.Equal(t, "SKIP", tr.advances[1].Decision.Kind)
	require.Equal(t, TradeQuote{Mark: "100", Exec: "101"}, tr.advances[0].Quotes["A"])
	// 策略状态：首期为空；第二期带着第一期的状态；跳过的周期不改变状态，所以第三期仍是 N=1。
	require.Equal(t, []string{"", `{"N":1}`, `{"N":1}`, `{"N":2}`}, s.states)
}

// 中断后续跑：已完成的周期不重做，缺的阶段按记录补齐；Trade 失败的那一期用记录里的命令原样重发，不重新求值。
func TestRunResumesFromRecords(t *testing.T) {
	r, f, s, tr := newRunner(t)
	tr.failNext = errors.New("trade 暂时不可达")
	_, err := r.Run(context.Background(), spec(3))
	require.Error(t, err, "第一期 Trade 推进失败")
	run, err := r.Store.GetRun("r1")
	require.NoError(t, err)
	require.Equal(t, StatusFailed, run.Status)
	require.Contains(t, run.Error, "trade 暂时不可达")
	rec, err := r.Store.GetRecord("r1", t0.Unix())
	require.NoError(t, err)
	require.False(t, rec.TradeAcked)
	require.Equal(t, 1, len(s.states), "决策已经落库，失败时只求值过一次")

	run, err = r.Run(context.Background(), spec(3))
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, run.Status)
	require.Len(t, s.states, 3, "续跑不重新求值第一期：三期共三次求值")
	require.Len(t, tr.advances, 3)
	require.Equal(t, t0.Add(time.Minute).UnixMilli(), tr.advances[0].Period)
	// 完成后再跑一次直接返回，不再调用任何模块。
	steps := len(f.steps)
	again, err := r.Run(context.Background(), spec(3))
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, again.Status)
	require.Len(t, f.steps, steps)
}

// 因子引擎重启（会话 404）：重新建立会话再取；策略版本变了不能续跑；规格不同的同名回放被拒绝。
func TestRunReopensFactorSessionAndRejectsDrift(t *testing.T) {
	r, f, s, _ := newRunner(t)
	f.failOnce[t0.Add(time.Minute).Format(time.RFC3339)] = &HTTPError{Module: "因子引擎", Status: http.StatusNotFound, Text: "会话不存在"}
	run, err := r.Run(context.Background(), spec(3))
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, run.Status)
	require.GreaterOrEqual(t, f.opens, 2, "会话丢失后重新建立")

	r2, _, s2, _ := newRunner(t)
	_, err = r2.Run(context.Background(), spec(2))
	require.NoError(t, err)
	s2.hash = "dsl2"
	other := spec(2)
	other.To = t0.Add(3 * time.Minute).Format(time.RFC3339)
	_, err = r2.Run(context.Background(), other)
	require.ErrorContains(t, err, "规格不同")
	_ = s
}

func TestRunRejectsStrategyDriftOnResume(t *testing.T) {
	r, _, s, tr := newRunner(t)
	tr.failNext = errors.New("boom")
	_, err := r.Run(context.Background(), spec(2))
	require.Error(t, err)
	s.hash = "dsl2"
	_, err = r.Run(context.Background(), spec(2))
	require.ErrorContains(t, err, "策略或因子定义发生了变化")
}

// 暂停后等待，继续后接着推进；取消后返回取消状态并停止。
func TestRunPauseResumeAndCancel(t *testing.T) {
	r, _, _, tr := newRunner(t)
	paused := make(chan struct{})
	r.Progress = func(run Run, _ StepTiming) {
		if run.PeriodsDone == 1 {
			ok, err := r.Store.Transition("r1", StatusPaused, StatusRunning)
			require.NoError(t, err)
			require.True(t, ok)
			close(paused)
		}
	}
	finished := make(chan Run, 1)
	go func() {
		run, _ := r.Run(context.Background(), spec(3))
		finished <- run
	}()
	<-paused
	time.Sleep(50 * time.Millisecond)
	tr.mu.Lock()
	got := len(tr.advances)
	tr.mu.Unlock()
	require.Equal(t, 1, got, "暂停后不再推进")
	ok, err := r.Store.Transition("r1", StatusRunning, StatusPaused)
	require.NoError(t, err)
	require.True(t, ok)
	select {
	case run := <-finished:
		require.Equal(t, StatusCompleted, run.Status)
	case <-time.After(5 * time.Second):
		t.Fatal("继续后应当跑完")
	}

	r2, _, _, _ := newRunner(t)
	r2.Progress = func(run Run, _ StepTiming) {
		if run.PeriodsDone == 1 {
			_, _ = r2.Store.Transition("r1", StatusCancelled, StatusRunning)
		}
	}
	run, err := r2.Run(context.Background(), spec(3))
	require.ErrorIs(t, err, errCancelled)
	require.Equal(t, StatusCancelled, run.Status)
	require.Equal(t, 1, run.PeriodsDone)
}

func TestBuildSummary(t *testing.T) {
	periods := []TradeResult{
		{Period: 1, Kind: "UPDATE_TARGET", Equity: "100000", Fees: "10", Turnover: "50000", Orders: []TradeOrder{{Status: "FILLED"}}},
		{Period: 2, Kind: "SKIP", Equity: "120000"},
		{Period: 3, Kind: "UPDATE_TARGET", Equity: "90000", Fees: "5", Turnover: "10000", Orders: []TradeOrder{{Status: "FILLED"}, {Status: "REJECTED"}}},
		{Period: 4, Kind: "SKIP", Equity: "99000"},
	}
	s, err := BuildSummary("r", 100000, periods)
	require.NoError(t, err)
	require.Equal(t, 4, s.Periods)
	require.InDelta(t, -0.01, s.TotalReturn, 1e-9)
	require.InDelta(t, 0.25, s.MaxDrawdown, 1e-9, "峰值 12 万、谷值 9 万")
	require.InDelta(t, 15, s.Fees, 1e-9)
	require.Equal(t, 3, s.Orders)
	require.Equal(t, 1, s.RejectedOrders)
	require.Equal(t, 2, s.SkippedPeriods)
}
