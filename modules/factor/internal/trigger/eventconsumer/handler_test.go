package eventconsumer

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type handlerTaskLocator struct {
	set     domain.FactorTask
	factors []domain.FactorDef
	found   bool
	err     error
	// extra 是同一源数据集上的其他启用任务（各策略实例的私有任务）。
	extra []domain.EngineTask
}

func (s *handlerTaskLocator) all() []domain.EngineTask {
	var out []domain.EngineTask
	if s.found {
		out = append(out, domain.EngineTask{Set: s.set, Members: domain.AsMembers(s.factors...), ResultReady: true})
	}
	return append(out, s.extra...)
}

func (s *handlerTaskLocator) EnabledSetsByDataset(context.Context, string, string, string) ([]domain.EngineTask, error) {
	return s.all(), s.err
}

func (s *handlerTaskLocator) EnabledTask(_ context.Context, taskID string) (domain.EngineTask, bool, error) {
	for _, set := range s.all() {
		if set.Set.TaskID == taskID {
			return set, true, s.err
		}
	}
	return domain.EngineTask{}, false, s.err
}

type handlerParamSelector struct {
	mu      sync.Mutex
	periods []time.Time
	sets    []string
	done    []string
	err     error
}

func (f *handlerParamSelector) Select(_ context.Context, set domain.FactorTask, members []domain.MemberDef, period time.Time) (map[string]domain.ParamSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.periods, f.sets = append(f.periods, period), append(f.sets, set.TaskID)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]domain.ParamSnapshot, len(members))
	for _, member := range members {
		out[member.Key] = domain.ParamSnapshot{"window": {Default: 30}}
	}
	return out, nil
}

func (f *handlerParamSelector) Done(_ context.Context, taskID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, taskID)
	return nil
}

func (s *handlerTaskLocator) FilterSubjects(context.Context) ([]string, error) { return nil, nil }

type handlerPeriodStore struct {
	mu     sync.Mutex
	exists bool
	// existsFor 非空时按结果数据集判断“这一期已算过”，用来模拟多个任务里只有部分已经写出。
	existsFor map[string]bool
	err       error
	calls     int
}

func (s *handlerPeriodStore) ComputedExists(_ context.Context, _, resultDatasetID, _ string, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.existsFor != nil {
		return s.existsFor[resultDatasetID], s.err
	}
	return s.exists, s.err
}

func (s *handlerPeriodStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "volume"}, nil
}

type handlerRunner struct {
	mu     sync.Mutex
	plans  []pipeline.Plan
	out    pipeline.Outcome
	err    error
	called chan struct{}
	// errFor 非空时按任务返回错误（优先于 err）；hook 在记录计划之后、返回之前调用，用来制造并发等待。
	errFor map[string]error
	hook   func(pipeline.Plan)
}

func (r *handlerRunner) Run(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	r.mu.Unlock()
	if r.called != nil {
		r.called <- struct{}{}
	}
	if r.hook != nil {
		r.hook(plan)
	}
	if err, ok := r.errFor[plan.Set.TaskID]; ok {
		return r.out, err
	}
	return r.out, r.err
}

type testTaskLocks struct {
	mu     sync.Mutex
	locked map[string]int
}

func (l *testTaskLocks) LockContext(_ context.Context, taskID string) (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locked == nil {
		l.locked = make(map[string]int)
	}
	l.locked[taskID]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.locked[taskID]--
	}, nil
}

func TestHandlerAcksWhenNoEnabledTask(t *testing.T) {
	runner := new(handlerRunner)
	handler := NewHandler(&handlerTaskLocator{}, &handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-1"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerGateClosedAcksWithoutComputing(t *testing.T) {
	set := enabledTestTask()
	periodStore := &handlerPeriodStore{}
	runner := new(handlerRunner)
	handler := NewHandler(&handlerTaskLocator{set: set, found: true}, periodStore, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	open := false
	handler.SetGate(func(string) bool { return open })

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-gate-1"))
	require.Equal(t, jetstream.ACK, result.Decision)
	require.Empty(t, runner.plans, "门禁关闭时不计算")

	open = true
	result = handler.Handle(context.Background(), collectorDelivery(t, "event-gate-2"))
	require.Equal(t, jetstream.ACK, result.Decision)
	require.NotEmpty(t, runner.plans, "门禁打开后正常计算")
}

func TestHandlerAcksWhenComputedAlreadyExists(t *testing.T) {
	set := enabledTestTask()
	periodStore := &handlerPeriodStore{exists: true}
	runner := new(handlerRunner)
	handler := NewHandler(&handlerTaskLocator{set: set, found: true}, periodStore, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-2"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Equal(t, 1, periodStore.calls)
	require.Empty(t, runner.plans)
}

func TestHandlerNaksOnErrInfra(t *testing.T) {
	set := enabledTestTask()
	runner := &handlerRunner{err: errors.Join(errors.New("wrapped"), storageio.ErrInfra)}
	handler := NewHandler(&handlerTaskLocator{set: set, found: true}, &handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{NakDelay: 3 * time.Second})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-3"))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Equal(t, 3*time.Second, result.Delay)
	require.ErrorIs(t, result.Err, storageio.ErrInfra)
}

func TestHandlerAcksDegradedOutcome(t *testing.T) {
	set := enabledTestTask()
	runner := &handlerRunner{out: pipeline.Outcome{Status: "degraded", FailedSubjects: []string{"ETH"}}}
	handler := NewHandler(&handlerTaskLocator{set: set, found: true}, &handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-4"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Len(t, runner.plans, 1)
	plan := runner.plans[0]
	require.Equal(t, pipeline.ModeLive, plan.Mode)
	require.Equal(t, "event-4", plan.TriggerEventID)
	require.Equal(t, []string{"BTC", "ETH"}, plan.Expected)
	require.Equal(t, []string{"BTC"}, plan.Available)
	require.Equal(t, []string{"ETH"}, plan.UpstreamFailed)
	require.Equal(t, []string{"close", "volume"}, plan.CarryColumns)
	require.True(t, plan.WriteCarry)
}

// 同一源数据集上的多个任务各算一份；每个任务开算前取得自己这一期的参数并带进计划，算完（完成标记写出）后清掉进行中记录。
func TestHandlerFansOutToEveryTaskAndSelectsParams(t *testing.T) {
	first := enabledTestTask()
	second := enabledTestTask()
	second.TaskID, second.ResultDatasetID = "ftask_second", "dataset_factor_second"
	factor := domain.FactorDef{FactorID: "f", InputColumns: []string{"close"}}
	locator := &handlerTaskLocator{set: first, factors: []domain.FactorDef{factor}, found: true,
		extra: []domain.EngineTask{{Set: second, Members: []domain.MemberDef{{Key: "fast", Factor: factor, Kind: domain.MemberKindVar}}, ResultReady: true}}}
	runner := new(handlerRunner)
	handler := NewHandler(locator, &handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	selector := new(handlerParamSelector)
	handler.SetParamSelector(selector)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-fan"))
	require.Equal(t, jetstream.ACK, result.Decision)
	require.Len(t, runner.plans, 2)
	planned := map[string]pipeline.Plan{}
	for _, plan := range runner.plans {
		planned[plan.Set.TaskID] = plan
	}
	require.Contains(t, planned, first.TaskID)
	require.Contains(t, planned, second.TaskID)
	require.Equal(t, 30.0, planned[second.TaskID].Params["fast"].Value("window", "BTC"))
	require.Equal(t, 30.0, planned[first.TaskID].Params["f"].Value("window", "BTC"))
	require.ElementsMatch(t, []string{first.TaskID, second.TaskID}, selector.sets)
	require.ElementsMatch(t, []string{first.TaskID, second.TaskID}, selector.done, "完成标记写出后清掉进行中记录")
	require.Equal(t, time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC), selector.periods[0])
}

// 取参失败是基础设施错误：事件重投，不按默认参数硬算。
func TestHandlerRetriesWhenParamSelectionFails(t *testing.T) {
	runner := new(handlerRunner)
	handler := NewHandler(&handlerTaskLocator{set: enabledTestTask(), factors: []domain.FactorDef{{FactorID: "f", InputColumns: []string{"close"}}}, found: true},
		&handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{NakDelay: time.Second})
	t.Cleanup(handler.Close)
	selector := &handlerParamSelector{err: errors.New("mgr down")}
	handler.SetParamSelector(selector)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-select"))
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorIs(t, result.Err, storageio.ErrInfra)
	require.Empty(t, runner.plans)
}

// 结果数据集还没就绪的任务不计算，事件重投；其他任务照常计算。
func TestHandlerRetriesWhenAnyTaskResultIsNotReady(t *testing.T) {
	pending := enabledTestTask()
	pending.TaskID = "ftask_pending"
	runner := new(handlerRunner)
	handler := NewHandler(&handlerTaskLocator{set: enabledTestTask(), found: true, extra: []domain.EngineTask{{Set: pending}}},
		&handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{NakDelay: time.Second})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-pending"))
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Len(t, runner.plans, 1)
	require.Equal(t, enabledTestTask().TaskID, runner.plans[0].Set.TaskID)
}

func TestHandlerTerminatesMalformedEvent(t *testing.T) {
	handler := NewHandler(&handlerTaskLocator{}, &handlerPeriodStore{}, new(handlerRunner), new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	result := handler.Handle(context.Background(), &jetstream.Delivery{RawData: []byte("not-an-event")})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
}

func TestFilterSubjectsFollowEnabledTasks(t *testing.T) {
	sets := &mutableFilterSetSource{t: t, sets: []domain.FactorTask{enabledTestTask()}}
	consumer := &Consumer{sets: sets}
	require.NoError(t, consumer.RefreshFilters(context.Background()))
	barsSubject := collectorPeriodSubject(t, "crypto", "bars")
	quotesSubject := collectorPeriodSubject(t, "crypto", "quotes")
	want := []string{barsSubject, quotesSubject}
	slices.Sort(want)
	require.Equal(t, []string{barsSubject}, consumer.CurrentFilterSubjects())

	sets.sets = append(sets.sets, domain.FactorTask{
		SpaceID: "crypto", SourceDatasetID: "quotes", Freq: "5m", Status: domain.TaskStatusEnabled,
	})
	consumer.SetsChanged()

	require.True(t, slices.Equal(want, consumer.CurrentFilterSubjects()))
	require.Equal(t, want, consumer.CurrentFilterSubjects())
}

type mutableFilterSetSource struct {
	t    *testing.T
	sets []domain.FactorTask
}

func (s *mutableFilterSetSource) FilterSubjects(context.Context) ([]string, error) {
	var filters []string
	for _, set := range s.sets {
		if set.Status == domain.TaskStatusEnabled {
			filters = append(filters, collectorPeriodSubject(s.t, set.SpaceID, set.SourceDatasetID))
		}
	}
	return trigger.NormalizeFilters(filters), nil
}

func (s *mutableFilterSetSource) EnabledSetsByDataset(_ context.Context, spaceID, datasetID, freq string) ([]domain.EngineTask, error) {
	var out []domain.EngineTask
	for _, set := range s.sets {
		if set.SpaceID == spaceID && set.SourceDatasetID == datasetID && set.Freq == freq && set.Status == domain.TaskStatusEnabled {
			out = append(out, domain.EngineTask{Set: set, ResultReady: true})
		}
	}
	return out, nil
}

func (s *mutableFilterSetSource) EnabledTask(_ context.Context, taskID string) (domain.EngineTask, bool, error) {
	for _, set := range s.sets {
		if set.TaskID == taskID && set.Status == domain.TaskStatusEnabled {
			return domain.EngineTask{Set: set, ResultReady: true}, true, nil
		}
	}
	return domain.EngineTask{}, false, nil
}

func enabledTestTask() domain.FactorTask {
	return domain.FactorTask{
		TaskID: "ftask_bars_1m", SpaceID: "crypto", Name: "行情因子", SourceDatasetID: "bars", Freq: "1m",
		Status: domain.TaskStatusEnabled, ResultDatasetID: "dataset_factor_bars_1m",
	}
}

func collectorDelivery(t *testing.T, eventID string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	period := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	payload := &storagepb.CollectorPeriodCompleted{
		DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), Status: "degraded",
		BatchId: "batch-1", ConfigSnapshotId: "config-1", ExpectedScopeRef: "scope-1",
		UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"ETH"},
		CollectedAt: timestamppb.New(period.Add(time.Minute)),
	}
	raw, err := registry.MarshalMessage(events.CollectorPeriodCompleted, payload, events.PublishOptions{
		EventID: eventID, OccurredAt: period.Add(time.Minute), SpaceID: "crypto", SubjectID: "bars",
	})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.CollectorPeriodCompleted, "crypto", "bars")
	require.NoError(t, err)
	return &jetstream.Delivery{Subject: subject, RawData: raw, RawMessageID: eventID, ContentType: events.ContentType}
}

func collectorPeriodSubject(t *testing.T, spaceID, datasetID string) string {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.CollectorPeriodCompleted, spaceID, datasetID)
	require.NoError(t, err)
	return subject
}

// secondTask 是同一源数据集上的另一个启用任务。
func secondTask() domain.EngineTask {
	set := enabledTestTask()
	set.TaskID, set.ResultDatasetID = "ftask_second", "dataset_factor_second"
	return domain.EngineTask{Set: set, Members: domain.AsMembers(domain.FactorDef{FactorID: "f", InputColumns: []string{"close"}}), ResultReady: true}
}

// 同一事件的各任务并发计算：两个任务的开算时间重叠，每个任务都要等到另一个也开始才能继续，串行分发会超时。
func TestHandlerComputesTasksConcurrently(t *testing.T) {
	var started sync.WaitGroup
	started.Add(2)
	overlap := make(chan struct{})
	go func() { started.Wait(); close(overlap) }()
	runner := &handlerRunner{hook: func(pipeline.Plan) {
		started.Done()
		select {
		case <-overlap:
		case <-time.After(5 * time.Second):
		}
	}}
	handler := NewHandler(&handlerTaskLocator{set: enabledTestTask(), found: true, extra: []domain.EngineTask{secondTask()}},
		&handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-parallel"))
	require.Equal(t, jetstream.ACK, result.Decision)
	select {
	case <-overlap:
	default:
		t.Fatal("两个任务的开算时间没有重叠：任务不是并发分发的")
	}
	require.Len(t, runner.plans, 2)
}

// 一个任务失败不影响另一个：另一个照常写出；事件重投后已写出的任务被跳过，只重算失败的任务。
func TestHandlerOneTaskFailureKeepsTheOtherAndRedeliverySkipsIt(t *testing.T) {
	first, second := enabledTestTask(), secondTask()
	runner := &handlerRunner{errFor: map[string]error{first.TaskID: errors.Join(errors.New("读源数据失败"), storageio.ErrInfra)}}
	periodStore := &handlerPeriodStore{existsFor: map[string]bool{}}
	handler := NewHandler(&handlerTaskLocator{set: first, found: true, extra: []domain.EngineTask{second}}, periodStore, runner, new(testTaskLocks), HandlerConfig{NakDelay: time.Second})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-partial"))
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorIs(t, result.Err, storageio.ErrInfra)
	planned := map[string]int{}
	for _, plan := range runner.plans {
		planned[plan.Set.TaskID]++
	}
	require.Equal(t, map[string]int{first.TaskID: 1, second.Set.TaskID: 1}, planned, "失败的任务不拖住另一个任务")

	// 重投：第二个任务的完成标记已经写出，只重算第一个。
	periodStore.existsFor[second.Set.ResultDatasetID] = true
	delete(runner.errFor, first.TaskID)
	result = handler.Handle(context.Background(), collectorDelivery(t, "event-partial"))
	require.Equal(t, jetstream.ACK, result.Decision)
	planned = map[string]int{}
	for _, plan := range runner.plans {
		planned[plan.Set.TaskID]++
	}
	require.Equal(t, map[string]int{first.TaskID: 2, second.Set.TaskID: 1}, planned, "已写出的任务在重投时被跳过")
}

// 同一任务内部仍然串行：同一个任务的两个事件并发到达时，两次计算不重叠。
func TestHandlerKeepsOnePeriodAtATimePerTask(t *testing.T) {
	var active, peak atomic.Int32
	runner := &handlerRunner{hook: func(pipeline.Plan) {
		peak.Store(max(peak.Load(), active.Add(1)))
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
	}}
	handler := NewHandler(&handlerTaskLocator{set: enabledTestTask(), found: true}, &handlerPeriodStore{}, runner, new(testTaskLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	var wg sync.WaitGroup
	for _, id := range []string{"event-serial-1", "event-serial-2", "event-serial-3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.Equal(t, jetstream.ACK, handler.Handle(context.Background(), collectorDelivery(t, id)).Decision)
		}()
	}
	wg.Wait()
	require.Len(t, runner.plans, 3)
	require.EqualValues(t, 1, peak.Load(), "同一任务的期次不能并行计算")
}
