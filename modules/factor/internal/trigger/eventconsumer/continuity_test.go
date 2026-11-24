package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// periodRows 是读回的假 Storage：每期哪些标的有行。
type periodRows struct {
	mu      sync.Mutex
	present map[int64]map[string]bool
}

func (p *periodRows) set(period time.Time, subjects ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.present == nil {
		p.present = map[int64]map[string]bool{}
	}
	p.present[period.Unix()] = map[string]bool{}
	for _, subject := range subjects {
		p.present[period.Unix()][subject] = true
	}
}

func (p *periodRows) RealRowsAt(_ context.Context, _ domain.FactorTask, subjects []string, period time.Time) (map[string]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]bool{}
	for _, subject := range subjects {
		if p.present[period.Unix()][subject] {
			out[subject] = true
		}
	}
	return out, nil
}

var continuityBase = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)

func subjectsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("S%02d", i)
	}
	return out
}

func minus(all []string, drop ...string) []string {
	skip := map[string]bool{}
	for _, value := range drop {
		skip[value] = true
	}
	var out []string
	for _, value := range all {
		if !skip[value] {
			out = append(out, value)
		}
	}
	return out
}

func periodDelivery(t *testing.T, eventID string, period time.Time, universe, failed []string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	status := "complete"
	if len(failed) > 0 {
		status = "degraded"
	}
	payload := &storagepb.DatasetPeriodCompleted{
		DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), Status: status, Source: "ledger", SnapshotRef: "scope-1",
		UniverseSubjectIds: universe, FailedSubjects: failed,
		CompletedAt: timestamppb.New(period.Add(time.Minute)),
	}
	raw, err := registry.MarshalMessage(events.DatasetPeriodCompleted, payload, events.PublishOptions{EventID: eventID, OccurredAt: period.Add(time.Minute), SpaceID: "crypto", SubjectID: "bars"})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, "crypto", "bars")
	require.NoError(t, err)
	return &jetstream.Delivery{Subject: subject, RawData: raw, RawMessageID: eventID, ContentType: events.ContentType}
}

type continuityHarness struct {
	t       *testing.T
	handler *Handler
	runner  *handlerRunner
	rows    *periodRows
	store   *watermark.Store
	cont    *Continuity
	now     time.Time
	taskID  string
}

func newContinuityHarness(t *testing.T) *continuityHarness {
	set := enabledTestTask()
	h := &continuityHarness{t: t, runner: new(handlerRunner), rows: &periodRows{}, now: continuityBase.Add(time.Hour), taskID: set.TaskID}
	h.handler = NewHandler(&handlerTaskLocator{set: set, factors: []domain.FactorDef{{FactorID: "f", InputColumns: []string{"close"}}}, found: true}, &handlerPeriodStore{}, h.runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(h.handler.Close)
	store, err := watermark.Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	h.store = store
	h.cont, err = NewContinuity(ContinuityConfig{Store: store, Local: h.rows, Now: func() time.Time { return h.now }})
	require.NoError(t, err)
	h.handler.SetContinuity(h.cont)
	return h
}

func (h *continuityHarness) event(minute int, universe, failed []string) jetstream.HandlerResult {
	return h.handler.Handle(context.Background(), periodDelivery(h.t, fmt.Sprintf("event-%d", minute), continuityBase.Add(time.Duration(minute)*time.Minute), universe, failed))
}

func (h *continuityHarness) state() watermark.State {
	state, _ := h.store.Get(h.taskID)
	return state
}

func (h *continuityHarness) computed() []time.Time {
	var out []time.Time
	for _, plan := range h.runner.plans {
		out = append(out, plan.PeriodTime)
	}
	return out
}

func minuteAt(minute int) time.Time { return continuityBase.Add(time.Duration(minute) * time.Minute) }

// 收到 t0+3 的事件、t0+1 与 t0+2 读回齐全：依次算 t0+1、t0+2、t0+3，水位连续前进。
func TestContinuityCatchesUpIntermediatePeriodsInOrder(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	require.Equal(t, jetstream.ACK, h.event(0, universe, nil).Decision)
	require.Equal(t, minuteAt(0), h.state().CommittedThrough, "第一次见到任务：从这一期起保证连续")
	h.rows.set(minuteAt(1), universe...)
	h.rows.set(minuteAt(2), universe...)
	require.Equal(t, jetstream.ACK, h.event(3, universe, nil).Decision)
	require.Equal(t, []time.Time{minuteAt(0), minuteAt(1), minuteAt(2), minuteAt(3)}, h.computed())
	require.Equal(t, CatchupEventID(h.taskID, minuteAt(1)), h.runner.plans[1].TriggerEventID, "没有事件的周期用合成的触发事件")
	require.Equal(t, "event-3", h.runner.plans[3].TriggerEventID)
	require.Equal(t, minuteAt(3), h.state().CommittedThrough)
	// 重复投递的旧事件不再计算。
	require.Equal(t, jetstream.ACK, h.event(2, universe, nil).Decision)
	require.Len(t, h.runner.plans, 4)
}

// 零提交周期阻塞水位，期间不调用计算；读回补齐后按顺序补算，水位追上。
func TestContinuityBlocksOnAZeroCommitPeriodUntilBackfilled(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	h.rows.set(minuteAt(1)) // 零提交：一行都没有
	require.Equal(t, jetstream.ACK, h.event(2, universe, nil).Decision, "阻塞也 ACK，不占待确认名额")
	require.Equal(t, []time.Time{minuteAt(0)}, h.computed(), "不能用缺根的窗口算后面的周期")
	state := h.state()
	require.Equal(t, minuteAt(0), state.CommittedThrough)
	require.Equal(t, minuteAt(2), state.Target)
	require.Equal(t, minuteAt(1), state.BlockedPeriod)
	require.Equal(t, "no_rows", state.BlockedReason)
	require.Equal(t, h.now, state.BlockedSince)

	// 追赶（保底轮询或K 线补写事件）时读回仍缺：不动。
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Len(t, h.runner.plans, 1)
	// 补采补齐 t0+1，t0+2 也读回齐全：按顺序补算，水位追上，阻塞清除。
	h.rows.set(minuteAt(1), universe...)
	h.rows.set(minuteAt(2), universe...)
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, []time.Time{minuteAt(0), minuteAt(1), minuteAt(2)}, h.computed())
	require.Equal(t, minuteAt(2), h.state().CommittedThrough)
	require.True(t, h.state().BlockedSince.IsZero())
}

// 当期降级、缺失在阈值以内：照常算，缺的标的作为上游失败交给账本，水位前进。
func TestContinuityComputesADegradedPeriodWithinTheThreshold(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	require.Equal(t, jetstream.ACK, h.event(1, universe, []string{"S03"}).Decision)
	require.Len(t, h.runner.plans, 2)
	require.Equal(t, []string{"S03"}, h.runner.plans[1].UpstreamFailed)
	require.Equal(t, minuteAt(1), h.state().CommittedThrough)
}

// 缺失超过阈值（只断开一个市场的连接）：阻塞；补到阈值以下后整期计算，水位前进。
func TestContinuityBlocksAboveTheThresholdAndRecomputesAfterBackfill(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	missing := []string{"S00", "S01", "S02", "S03", "S04"}
	h.rows.set(minuteAt(1), minus(universe, missing...)...)
	h.event(1, universe, missing)
	require.Len(t, h.runner.plans, 1)
	require.Equal(t, "missing_over_threshold", h.state().BlockedReason)
	// 事件已经 ACK，之后靠读回：补到只缺 1 个（5%）。
	h.rows.set(minuteAt(1), minus(universe, "S00")...)
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Len(t, h.runner.plans, 2)
	require.Equal(t, []string{"S00"}, h.runner.plans[1].UpstreamFailed)
	require.Equal(t, minuteAt(1), h.state().CommittedThrough)
}

// 报告完成标记遇到重试也不会通过的错误（例如标记超过体积上限、Storage 判为无效请求）：水位停在这一期并记成阻塞
// （compute_failed），水位阻塞的监控能看到；基础设施错误不记阻塞，照常重试。问题排除后追赶算完这一期，阻塞清除。
func TestContinuityMarksNonTransientComputeFailureAsBlocked(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)

	h.runner.err = fmt.Errorf("%w: storage unavailable", storageio.ErrInfra)
	require.Equal(t, jetstream.RETRY, h.event(1, universe, nil).Decision)
	require.True(t, h.state().BlockedSince.IsZero(), "基础设施错误只重试，不记阻塞")

	h.runner.err = errors.New("report factor period computed: 因子周期完成标记超过体积上限")
	require.Equal(t, jetstream.TERM, h.event(1, universe, nil).Decision)
	state := h.state()
	require.Equal(t, minuteAt(0), state.CommittedThrough)
	require.Equal(t, minuteAt(1), state.BlockedPeriod)
	require.Equal(t, "compute_failed", state.BlockedReason)
	require.Equal(t, h.now, state.BlockedSince)

	// 追赶（这一期读回齐全）时仍失败：阻塞开始的时刻不变。
	h.rows.set(minuteAt(1), universe...)
	h.now = h.now.Add(time.Minute)
	require.Error(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, h.now.Add(-time.Minute), h.state().BlockedSince)
	require.Equal(t, "compute_failed", h.state().BlockedReason)

	h.runner.err = nil
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, minuteAt(1), h.state().CommittedThrough)
	require.True(t, h.state().BlockedSince.IsZero())
	require.Empty(t, h.state().BlockedReason)
}

// 补采确认行情源没有的标的不计入缺失、不阻塞，也不作为上游失败记账。
func TestContinuityIgnoresConfirmedAbsences(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	missing := []string{"S00", "S01", "S02", "S03"}
	h.cont.NoteAbsent(h.taskID, minuteAt(1), minuteAt(2), time.Minute, missing)
	h.event(1, universe, missing)
	require.Len(t, h.runner.plans, 2)
	require.Empty(t, h.runner.plans[1].UpstreamFailed)
	require.Equal(t, minuteAt(1), h.state().CommittedThrough)
}

// 重启：状态文件恢复水位与目标，追赶继续。
func TestContinuityResumesAfterRestart(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	h.rows.set(minuteAt(1))
	h.event(2, universe, nil)
	reopened, err := watermark.Open(h.store.Path())
	require.NoError(t, err)
	cont, err := NewContinuity(ContinuityConfig{Store: reopened, Local: h.rows})
	require.NoError(t, err)
	h.handler.SetContinuity(cont)
	h.rows.set(minuteAt(1), universe...)
	h.rows.set(minuteAt(2), universe...)
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	state, _ := reopened.Get(h.taskID)
	require.Equal(t, minuteAt(2), state.CommittedThrough)
}

// 一次推进至多 MaxPeriods 期，剩下的交给追赶循环。
func TestContinuityBoundsOneAdvance(t *testing.T) {
	h := newContinuityHarness(t)
	h.cont.cfg.MaxPeriods = 2
	universe := subjectsN(20)
	h.event(0, universe, nil)
	for minute := 1; minute <= 5; minute++ {
		h.rows.set(minuteAt(minute), universe...)
	}
	h.event(5, universe, nil)
	require.Equal(t, minuteAt(2), h.state().CommittedThrough)
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, minuteAt(5), h.state().CommittedThrough)
}

// 确认没有的标的不参与计算（不读窗口、不记账本）；范围内全部确认没有时直接推进，不阻塞。
func TestContinuityExcludesConfirmedAbsencesFromComputation(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	h.cont.NoteAbsent(h.taskID, minuteAt(1), minuteAt(2), time.Minute, []string{"S00"})
	h.event(1, universe, []string{"S00"})
	require.NotContains(t, h.runner.plans[1].Expected, "S00")
	require.Len(t, h.runner.plans[1].Expected, 19)

	h.cont.NoteAbsent(h.taskID, minuteAt(2), minuteAt(3), time.Minute, universe)
	h.event(2, universe, universe)
	require.Len(t, h.runner.plans, 2, "全部确认没有：这一期没有可算的")
	require.Equal(t, minuteAt(2), h.state().CommittedThrough)
	require.True(t, h.state().BlockedSince.IsZero())
}

// 计算完成、水位落盘前进程退出：重启后这一期沿用原触发事件 ID，已完成检查能认出它。
func TestContinuityReusesTheInFlightTriggerAfterACrash(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	state := h.state()
	state.Target, state.InFlightPeriod, state.InFlightTrigger = minuteAt(1), minuteAt(1), "event-1"
	require.NoError(t, h.store.Put(state))
	h.rows.set(minuteAt(1), universe...)
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, "event-1", h.runner.plans[1].TriggerEventID)
	require.Empty(t, h.state().InFlightTrigger)
	require.Equal(t, minuteAt(1), h.state().CommittedThrough)
}

// K 线补写事件（消费协程）与推进并发时没有数据竞争（配合 -race）。
func TestContinuityNoteAbsentConcurrentWithAdvance(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	for minute := 1; minute <= 20; minute++ {
		h.rows.set(minuteAt(minute), universe...)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			h.cont.NoteAbsent(h.taskID, minuteAt(1), minuteAt(20), time.Minute, []string{fmt.Sprintf("X%03d", i)})
		}
	}()
	h.event(20, universe, nil)
	<-done
	require.Equal(t, minuteAt(20), h.state().CommittedThrough)
}

// 实时门禁关闭（恢复追赶、暂停）时：只有追赶门禁放行，连续水位才会追赶；两个门禁都关则不动。
func TestAdvanceFollowsCatchupGateWhenRealtimeGateClosed(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.event(0, universe, nil)
	h.rows.set(minuteAt(1), universe...)
	h.rows.set(minuteAt(2), universe...)
	h.event(2, universe, nil) // 实时事件到 t0+2，t0+1 由追赶补算
	computed := len(h.runner.plans)

	// 之后的周期只在读回里，没有事件：模拟暂停期间错过的周期。
	h.rows.set(minuteAt(3), universe...)
	h.rows.set(minuteAt(4), universe...)
	h.handler.SetGate(func(string) bool { return false })
	catchup := false
	h.handler.SetCatchupGate(func(string) bool { return catchup })
	require.NoError(t, h.cont.NoteTarget(h.taskID, minuteAt(4)))

	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Len(t, h.runner.plans, computed, "两个门禁都关：不追赶")

	catchup = true
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Greater(t, len(h.runner.plans), computed, "追赶门禁放行：只靠追赶补算历史周期")
	require.Equal(t, minuteAt(4), h.state().CommittedThrough)
}

// 模块在运行、任务还没就绪（引擎重启后窗口载入中）时到达的周期：本期不算，但记下追赶目标；就绪后追赶循环直接补算，不等下一条实时事件。
// 模块暂停时（noteWhenClosed 返回 false）不记目标；从没算过的任务不建水位记录。
func TestNotReadyEventIsNotedAndCaughtUpAfterReady(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	ready, running := false, false
	h.handler.SetGate(func(string) bool { return ready })
	h.handler.SetNoteWhenClosed(func(string) bool { return running })

	running = true
	require.Equal(t, jetstream.ACK, h.event(0, universe, nil).Decision)
	_, noted := h.store.Get(h.taskID)
	require.False(t, noted, "从没算过的任务不建水位记录：连续性从就绪后的第一条实时事件起保证")

	// 任务算过一期（有水位记录）之后引擎重启、窗口重新载入：载入期间到达的周期只记目标。
	ready = true
	require.Equal(t, jetstream.ACK, h.event(0, universe, nil).Decision)
	require.Equal(t, []time.Time{minuteAt(0)}, h.computed())
	ready = false
	h.rows.set(minuteAt(1), universe...)
	h.rows.set(minuteAt(2), universe...)
	h.rows.set(minuteAt(3), universe...)
	require.Equal(t, jetstream.ACK, h.event(1, universe, nil).Decision)
	require.Equal(t, jetstream.ACK, h.event(2, universe, nil).Decision)
	require.Len(t, h.runner.plans, 1, "任务没就绪：本期不算")
	require.Equal(t, minuteAt(2), h.state().Target)
	require.Equal(t, minuteAt(0), h.state().CommittedThrough)

	running = false
	require.Equal(t, jetstream.ACK, h.event(3, universe, nil).Decision)
	require.Equal(t, minuteAt(2), h.state().Target, "模块暂停：不记追赶目标，由恢复追赶显式建立")
	running = true
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Len(t, h.runner.plans, 1, "没就绪时追赶循环也不算")

	ready = true
	require.NoError(t, h.handler.Advance(context.Background(), h.taskID))
	require.Equal(t, []time.Time{minuteAt(0), minuteAt(1), minuteAt(2)}, h.computed(), "就绪后追赶循环补算载入期间到达的周期")
	require.Equal(t, minuteAt(2), h.state().CommittedThrough)
}
