package eventconsumer

import (
	"context"
	"errors"
	"slices"
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

type handlerSetLocator struct {
	set     domain.FactorSet
	factors []domain.FactorDef
	found   bool
	err     error
	// extra 是同一源数据集上的其他启用集合（各策略实例的私有集合）。
	extra []domain.EngineSet
}

func (s *handlerSetLocator) all() []domain.EngineSet {
	var out []domain.EngineSet
	if s.found {
		out = append(out, domain.EngineSet{Set: s.set, Members: domain.AsMembers(s.factors...), ResultReady: true})
	}
	return append(out, s.extra...)
}

func (s *handlerSetLocator) EnabledSetsByDataset(context.Context, string, string, string) ([]domain.EngineSet, error) {
	return s.all(), s.err
}

func (s *handlerSetLocator) EnabledSet(_ context.Context, setID string) (domain.EngineSet, bool, error) {
	for _, set := range s.all() {
		if set.Set.SetID == setID {
			return set, true, s.err
		}
	}
	return domain.EngineSet{}, false, s.err
}

type handlerParamFreezer struct {
	periods []time.Time
	sets    []string
	err     error
}

func (f *handlerParamFreezer) Freeze(_ context.Context, set domain.FactorSet, members []domain.MemberDef, period time.Time) (map[string]domain.ParamFreeze, error) {
	f.periods, f.sets = append(f.periods, period), append(f.sets, set.SetID)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]domain.ParamFreeze, len(members))
	for _, member := range members {
		snapshot := domain.ParamSnapshot{"window": {Default: 30}}
		out[member.Alias] = domain.ParamFreeze{SetID: set.SetID, MemberAlias: member.Alias, PeriodTime: period,
			Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested}
	}
	return out, nil
}

func (s *handlerSetLocator) FilterSubjects(context.Context) ([]string, error) { return nil, nil }

type handlerPeriodStore struct {
	exists bool
	err    error
	calls  int
}

func (s *handlerPeriodStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	s.calls++
	return s.exists, s.err
}

func (s *handlerPeriodStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "volume"}, nil
}

type handlerRunner struct {
	plans  []pipeline.Plan
	out    pipeline.Outcome
	err    error
	called chan struct{}
}

func (r *handlerRunner) Run(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.plans = append(r.plans, plan)
	if r.called != nil {
		r.called <- struct{}{}
	}
	return r.out, r.err
}

type testSetLocks struct {
	locked map[string]int
}

func (l *testSetLocks) LockContext(_ context.Context, setID string) (func(), error) {
	if l.locked == nil {
		l.locked = make(map[string]int)
	}
	l.locked[setID]++
	return func() { l.locked[setID]-- }, nil
}

func TestHandlerAcksWhenNoEnabledSet(t *testing.T) {
	runner := new(handlerRunner)
	handler := NewHandler(&handlerSetLocator{}, &handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-1"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerGateClosedAcksWithoutComputing(t *testing.T) {
	set := enabledTestSet()
	periodStore := &handlerPeriodStore{}
	runner := new(handlerRunner)
	handler := NewHandler(&handlerSetLocator{set: set, found: true}, periodStore, runner, new(testSetLocks), HandlerConfig{})
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
	set := enabledTestSet()
	periodStore := &handlerPeriodStore{exists: true}
	runner := new(handlerRunner)
	handler := NewHandler(&handlerSetLocator{set: set, found: true}, periodStore, runner, new(testSetLocks), HandlerConfig{})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-2"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Equal(t, 1, periodStore.calls)
	require.Empty(t, runner.plans)
}

func TestHandlerNaksOnErrInfra(t *testing.T) {
	set := enabledTestSet()
	runner := &handlerRunner{err: errors.Join(errors.New("wrapped"), storageio.ErrInfra)}
	handler := NewHandler(&handlerSetLocator{set: set, found: true}, &handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{NakDelay: 3 * time.Second})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-3"))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Equal(t, 3*time.Second, result.Delay)
	require.ErrorIs(t, result.Err, storageio.ErrInfra)
}

func TestHandlerAcksDegradedOutcome(t *testing.T) {
	set := enabledTestSet()
	runner := &handlerRunner{out: pipeline.Outcome{Status: "degraded", FailedSubjects: []string{"ETH"}}}
	handler := NewHandler(&handlerSetLocator{set: set, found: true}, &handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{})
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

// 同一源数据集上的多个集合（各策略实例的私有集合）各算一份；每个集合开算前冻结自己这一期的参数并带进计划。
func TestHandlerFansOutToEverySetAndFreezesParams(t *testing.T) {
	first := enabledTestSet()
	second := enabledTestSet()
	second.SetID, second.ResultDatasetID = "fset_i_strategy", "dataset_factor_i_strategy"
	factor := domain.FactorDef{FactorID: "f", InputColumns: []string{"close"}}
	locator := &handlerSetLocator{set: first, factors: []domain.FactorDef{factor}, found: true,
		extra: []domain.EngineSet{{Set: second, Members: []domain.MemberDef{{Alias: "fast", Factor: factor}}, ResultReady: true}}}
	runner := new(handlerRunner)
	handler := NewHandler(locator, &handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	freezer := new(handlerParamFreezer)
	handler.SetParamFreezer(freezer)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-fan"))
	require.Equal(t, jetstream.ACK, result.Decision)
	require.Len(t, runner.plans, 2)
	planned := map[string]pipeline.Plan{}
	for _, plan := range runner.plans {
		planned[plan.Set.SetID] = plan
	}
	require.Contains(t, planned, first.SetID)
	require.Contains(t, planned, second.SetID)
	require.Equal(t, domain.ParamsRequested, planned[second.SetID].Params["fast"].Source)
	require.Equal(t, 30.0, planned[first.SetID].Params["f"].Snapshot.Value("window", "BTC"))
	require.ElementsMatch(t, []string{first.SetID, second.SetID}, freezer.sets)
	require.Equal(t, time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC), freezer.periods[0])
}

// 冻结失败是基础设施错误：事件重投，不按默认参数硬算。
func TestHandlerRetriesWhenFreezeFails(t *testing.T) {
	runner := new(handlerRunner)
	handler := NewHandler(&handlerSetLocator{set: enabledTestSet(), factors: []domain.FactorDef{{FactorID: "f", InputColumns: []string{"close"}}}, found: true},
		&handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{NakDelay: time.Second})
	t.Cleanup(handler.Close)
	handler.SetParamFreezer(&handlerParamFreezer{err: errors.New("mgr down")})

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-freeze"))
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorIs(t, result.Err, storageio.ErrInfra)
	require.Empty(t, runner.plans)
}

// 结果数据集还没就绪的集合不计算，事件重投；其他集合照常计算。
func TestHandlerRetriesWhenAnySetResultIsNotReady(t *testing.T) {
	pending := enabledTestSet()
	pending.SetID = "fset_pending"
	runner := new(handlerRunner)
	handler := NewHandler(&handlerSetLocator{set: enabledTestSet(), found: true, extra: []domain.EngineSet{{Set: pending}}},
		&handlerPeriodStore{}, runner, new(testSetLocks), HandlerConfig{NakDelay: time.Second})
	t.Cleanup(handler.Close)

	result := handler.Handle(context.Background(), collectorDelivery(t, "event-pending"))
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Len(t, runner.plans, 1)
	require.Equal(t, enabledTestSet().SetID, runner.plans[0].Set.SetID)
}

func TestHandlerTerminatesMalformedEvent(t *testing.T) {
	handler := NewHandler(&handlerSetLocator{}, &handlerPeriodStore{}, new(handlerRunner), new(testSetLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	result := handler.Handle(context.Background(), &jetstream.Delivery{RawData: []byte("not-an-event")})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
}

func TestFilterSubjectsFollowEnabledSets(t *testing.T) {
	sets := &mutableFilterSetSource{t: t, sets: []domain.FactorSet{enabledTestSet()}}
	consumer := &Consumer{sets: sets}
	require.NoError(t, consumer.RefreshFilters(context.Background()))
	barsSubject := collectorPeriodSubject(t, "crypto", "bars")
	quotesSubject := collectorPeriodSubject(t, "crypto", "quotes")
	want := []string{barsSubject, quotesSubject}
	slices.Sort(want)
	require.Equal(t, []string{barsSubject}, consumer.CurrentFilterSubjects())

	sets.sets = append(sets.sets, domain.FactorSet{
		SpaceID: "crypto", SourceDatasetID: "quotes", Freq: "5m", Status: domain.SetStatusEnabled,
	})
	consumer.SetsChanged()

	require.True(t, slices.Equal(want, consumer.CurrentFilterSubjects()))
	require.Equal(t, want, consumer.CurrentFilterSubjects())
}

type mutableFilterSetSource struct {
	t    *testing.T
	sets []domain.FactorSet
}

func (s *mutableFilterSetSource) FilterSubjects(context.Context) ([]string, error) {
	var filters []string
	for _, set := range s.sets {
		if set.Status == domain.SetStatusEnabled {
			filters = append(filters, collectorPeriodSubject(s.t, set.SpaceID, set.SourceDatasetID))
		}
	}
	return trigger.NormalizeFilters(filters), nil
}

func (s *mutableFilterSetSource) EnabledSetsByDataset(_ context.Context, spaceID, datasetID, freq string) ([]domain.EngineSet, error) {
	var out []domain.EngineSet
	for _, set := range s.sets {
		if set.SpaceID == spaceID && set.SourceDatasetID == datasetID && set.Freq == freq && set.Status == domain.SetStatusEnabled {
			out = append(out, domain.EngineSet{Set: set, ResultReady: true})
		}
	}
	return out, nil
}

func (s *mutableFilterSetSource) EnabledSet(_ context.Context, setID string) (domain.EngineSet, bool, error) {
	for _, set := range s.sets {
		if set.SetID == setID && set.Status == domain.SetStatusEnabled {
			return domain.EngineSet{Set: set, ResultReady: true}, true, nil
		}
	}
	return domain.EngineSet{}, false, nil
}

func enabledTestSet() domain.FactorSet {
	return domain.FactorSet{
		SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "bars", Freq: "1m",
		Status: domain.SetStatusEnabled, ResultDatasetID: "dataset_factor_bars_1m",
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
