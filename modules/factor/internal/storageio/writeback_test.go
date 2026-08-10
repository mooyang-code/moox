package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestWriteFactorPatchAllowsFactorToCreateNewSeriesTag(t *testing.T) {
	access := &fakeAccessClient{}
	client := &Client{access: access}
	at := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	written, err := client.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "spread-task", SpaceID: "crypto", TargetDataset: "spot_factor",
		SubjectID: "BTC-USDT", Freq: "1h",
		Factor: engine.FactorSpec{
			FactorID: "venue-spread", SourceHash: "source-hash", Outputs: []string{"spread"},
		},
	}, &engine.FactorResult{Rows: []engine.FactorResultRow{{
		DataTime: at, SeriesTag: "venue_pair:binance-okx",
		Values: map[string]any{"spread": 1.25},
	}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, written)
	row := access.writeReqs[0].GetRows()[0]
	require.Equal(t, at.Format(time.RFC3339Nano), row.GetKey().GetTimeSeries().GetDataTime())
	require.Equal(t, "venue_pair:binance-okx", row.GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "venue-spread", row.GetAttributes()["factor.id"].GetStringValue())
	require.Equal(t, "source-hash", row.GetAttributes()["factor.source_hash"].GetStringValue())
	require.InDelta(t, 1.25, row.GetFields()[0].GetValue().GetDoubleValue(), 1e-12)
}

func TestWriteFactorPatchClearsDynamicManifestRows(t *testing.T) {
	access := &fakeAccessClient{}
	manifests := &memoryManifest{}
	client := (&Client{access: access}).WithOutputManifests(manifests)
	at := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	task := &engine.FactorTask{TaskID: "task", BindingID: "binding", SpaceID: "crypto", ResultDatasetID: "result", SubjectID: "BTC", Freq: "1m", PeriodTime: at.Unix(), TriggerEventID: "ready-1", TriggeredAt: at, Factor: engine.FactorSpec{FactorID: "factor", SourceHash: "hash", Outputs: []string{"value"}}}
	result := func(tag string) *engine.FactorResult {
		return &engine.FactorResult{Rows: []engine.FactorResultRow{{DataTime: at, SeriesTag: tag, Values: map[string]any{"value": 1.0}}}}
	}
	_, err := client.WriteFactorPatch(context.Background(), task, result("A"))
	require.NoError(t, err)
	task.TriggerEventID = "ready-2"
	_, err = client.WriteFactorPatch(context.Background(), task, result("B"))
	require.NoError(t, err)
	task.TriggerEventID = "ready-3"
	_, err = client.WriteFactorPatch(context.Background(), task, &engine.FactorResult{})
	require.NoError(t, err)
	require.Len(t, access.writeReqs, 4)
	require.Equal(t, "A", access.writeReqs[1].GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "B", access.writeReqs[2].GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "B", access.writeReqs[3].GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "factor__value", access.writeReqs[3].GetRows()[0].GetFields()[0].GetFieldId())
	require.Empty(t, manifests.keys)
}

func TestDeterministicWriteIDIgnoresComputedAtButNotResultValues(t *testing.T) {
	at := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	task := &engine.FactorTask{BindingID: "binding", SpaceID: "crypto", ResultDatasetID: "result", SubjectID: "BTC", Freq: "1m", PeriodTime: at.Unix(), TriggerEventID: "ready-1", TriggeredAt: at, Factor: engine.FactorSpec{FactorID: "factor", SourceHash: "hash", Outputs: []string{"value"}}}
	result := &engine.FactorResult{Rows: []engine.FactorResultRow{{DataTime: at, Values: map[string]any{"value": 1.0}}}}
	first, _, err := buildFactorRows(task, result)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
	second, _, err := buildFactorRows(task, result)
	require.NoError(t, err)
	require.NotEqual(t, first[0].GetAttributes()["factor.computed_at"].GetStringValue(), second[0].GetAttributes()["factor.computed_at"].GetStringValue())
	require.Equal(t, deterministicWriteID(task, "upsert", first), deterministicWriteID(task, "upsert", second))

	changed, _, err := buildFactorRows(task, &engine.FactorResult{Rows: []engine.FactorResultRow{{DataTime: at, Values: map[string]any{"value": 2.0}}}})
	require.NoError(t, err)
	require.NotEqual(t, deterministicWriteID(task, "upsert", first), deterministicWriteID(task, "upsert", changed))
}

type memoryManifest struct{ keys []string }

func (m *memoryManifest) Get(context.Context, store.OutputManifestKey) ([]string, error) {
	return append([]string(nil), m.keys...), nil
}
func (m *memoryManifest) Replace(_ context.Context, _ store.OutputManifestKey, keys []string) error {
	m.keys = append([]string(nil), keys...)
	return nil
}
