package engine

import (
	"context"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

type localRowsFake struct{}

func (localRowsFake) RealRowsAt(context.Context, domain.FactorTask, []string, time.Time) (map[string]bool, error) {
	return map[string]bool{}, nil
}

type periodStoreFake struct{}

func (periodStoreFake) ComputedExists(context.Context, string, string, int64) (bool, error) {
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
	payload := &eventpb.DatasetPeriodCompleted{
		DatasetId: datasetID, Frequency: "1m", PeriodTime: period.Unix(), Status: "complete",
		Source: "ledger", SnapshotRef: "scope-1",
		UniverseSubjectIds: []string{"BTC"}, CompletedAt: period.Add(time.Minute).UnixMilli(),
	}
	raw, err := registry.MarshalMessage(events.DatasetPeriodCompleted, payload, events.PublishOptions{
		EventID: "event-1", OccurredAt: period.Add(time.Minute), SpaceID: "crypto", SubjectID: datasetID,
	})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, "crypto", datasetID)
	require.NoError(t, err)
	return &jetstream.Delivery{Subject: subject, RawData: raw, RawMessageID: "event-1", ContentType: events.ContentType}
}

func newCatalogHandler(t *testing.T, cache *CatalogCache, runner *planRecorder) *eventconsumer.Handler {
	t.Helper()
	wm, err := watermark.Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	cont, err := eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{Store: wm, Local: localRowsFake{}})
	require.NoError(t, err)
	handler, err := eventconsumer.NewHandler(cache, periodStoreFake{}, runner, setlock.New(), eventconsumer.HandlerConfig{Continuity: cont})
	require.NoError(t, err)
	t.Cleanup(handler.Close)
	return handler
}

func TestHandlerRetriesWhenTaskNotResultReady(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", false, "bias")}})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.RETRY, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerComputesReadyTask(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Len(t, runner.plans, 1)
	require.Equal(t, "ftask_a", runner.plans[0].Set.TaskID)
}

func TestHandlerAcksWhenTaskRemovedFromSnapshot(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-2"})
	require.NoError(t, err)
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.ACK, result.Decision)
	require.Empty(t, runner.plans)
}

func TestHandlerRetriesBeforeFirstCatalog(t *testing.T) {
	cache, _ := newTestCache(t)
	runner := &planRecorder{}

	result := newCatalogHandler(t, cache, runner).Handle(context.Background(), periodDelivery(t, "dataset_a"))

	require.Equal(t, jetstream.RETRY, result.Decision)
}
