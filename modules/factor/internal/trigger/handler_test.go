package trigger

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
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
}

func (s *handlerSetLocator) EnabledSetByDataset(context.Context, string, string, string) (domain.FactorSet, []domain.FactorDef, bool, error) {
	return s.set, append([]domain.FactorDef(nil), s.factors...), s.found, s.err
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

func TestHandlerTerminatesMalformedEvent(t *testing.T) {
	handler := NewHandler(&handlerSetLocator{}, &handlerPeriodStore{}, new(handlerRunner), new(testSetLocks), HandlerConfig{})
	t.Cleanup(handler.Close)
	result := handler.Handle(context.Background(), &jetstream.Delivery{RawData: []byte("not-an-event")})

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
}

func TestFilterSubjectsFollowEnabledSets(t *testing.T) {
	sets := &mutableFilterSetSource{sets: []domain.FactorSet{enabledTestSet()}}
	locator, err := NewStoreSetLocator(sets)
	require.NoError(t, err)
	consumer := &Consumer{sets: locator}
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

type mutableFilterSetSource struct{ sets []domain.FactorSet }

func (s *mutableFilterSetSource) ListSets(context.Context) ([]domain.FactorSet, error) {
	return append([]domain.FactorSet(nil), s.sets...), nil
}

func (s *mutableFilterSetSource) EnabledSetByDataset(_ context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error) {
	for _, set := range s.sets {
		if set.SpaceID == spaceID && set.SourceDatasetID == datasetID && set.Freq == freq && set.Status == domain.SetStatusEnabled {
			return set, nil, true, nil
		}
	}
	return domain.FactorSet{}, nil, false, nil
}

func enabledTestSet() domain.FactorSet {
	return domain.FactorSet{
		SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "bars", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, Status: domain.SetStatusEnabled, ResultDatasetID: "dataset_factor_bars_1m",
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
