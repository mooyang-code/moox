package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type periodStoreFake struct{}

func (periodStoreFake) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func (periodStoreFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func periodDelivery(t *testing.T, datasetID string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	period := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	payload := &storagepb.CollectorPeriodCompleted{
		DatasetId: datasetID, Frequency: "1m", PeriodTime: period.Unix(), Status: "complete",
		BatchId: "batch-1", ConfigSnapshotId: "config-1", ExpectedScopeRef: "scope-1",
		UniverseSubjectIds: []string{"BTC"}, CollectedAt: timestamppb.New(period.Add(time.Minute)),
	}
	raw, err := registry.MarshalMessage(events.CollectorPeriodCompleted, payload, events.PublishOptions{
		EventID: "event-1", OccurredAt: period.Add(time.Minute), SpaceID: "crypto", SubjectID: datasetID,
	})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.CollectorPeriodCompleted, "crypto", datasetID)
	require.NoError(t, err)
	return &jetstream.Delivery{Subject: subject, RawData: raw, RawMessageID: "event-1", ContentType: events.ContentType}
}

func newCatalogHandler(t *testing.T, cache *CatalogCache, runner *planRecorder) *eventconsumer.Handler {
	t.Helper()
	handler := eventconsumer.NewHandler(cache, periodStoreFake{}, runner, setlock.New(""), eventconsumer.HandlerConfig{})
	t.Cleanup(handler.Close)
	return handler
}

func TestHandlerRetriesWhenSetNotResultReady(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", false, "bias")}})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerComputesReadySet(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Len(t, runner.plans, 1)
	require.Equal(t, "fset_a", runner.plans[0].Set.SetID)
}

func TestHandlerAcksWhenSetRemovedFromSnapshot(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-2"})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerRetriesBeforeFirstCatalog(t *testing.T) {
	cache, _, _ := newTestCache(t)
	runner := &planRecorder{}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.RETRY, result.Decision)
}
