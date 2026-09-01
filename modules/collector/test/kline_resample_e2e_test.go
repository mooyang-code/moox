package test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type resampleE2ESource struct{}

func (resampleE2ESource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{
		DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Status: "active", Freqs: []string{"1m"},
		Attributes: map[string]string{"market_type": "spot"}, KeepDuration: "4320h",
	}, nil
}

func (resampleE2ESource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return []domain.DatasetSubject{{SubjectID: "BTC", Status: "active"}, {SubjectID: "ETH", Status: "active"}}, nil
}

type resampleE2EPrimary struct {
	mu      sync.Mutex
	rows    map[string]*storagepb.RowFieldValues
	targets map[string]*storagepb.RowFieldUpsert
	writes  int
}

func (p *resampleE2EPrimary) ReadFields(_ context.Context, keys []*storagepb.RowKey, _ []string, attrs []string) ([]*storagepb.RowFieldValues, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]*storagepb.RowFieldValues, 0, len(keys))
	for _, key := range keys {
		if row := p.rows[rowKeyText(key)]; row != nil {
			result = append(result, row)
			continue
		}
		if row := p.targets[rowKeyText(key)]; row != nil {
			result = append(result, &storagepb.RowFieldValues{Key: row.Key, Fields: row.Fields, Attributes: row.Attributes})
		}
	}
	_ = attrs
	return result, nil
}

func (p *resampleE2EPrimary) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, row := range rows {
		p.targets[rowKeyText(row.GetKey())] = row
		p.writes++
	}
	return nil
}

func rowKeyText(key *storagepb.RowKey) string {
	if key == nil || key.GetTimeSeries() == nil {
		return ""
	}
	ts := key.GetTimeSeries()
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", key.GetSpaceId(), key.GetDatasetId(), ts.GetSubjectId(), ts.GetFreq(), ts.GetDataTime(), ts.GetSeriesTag())
}

func e2eSourceRow(space, dataset, subject string, at time.Time, closeValue float64) *storagepb.RowFieldValues {
	double := func(name string, value float64) *storagepb.FieldValue {
		return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
	}
	return &storagepb.RowFieldValues{
		Key:    &storagepb.RowKey{SpaceId: space, DatasetId: dataset, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "venue:binance"}}},
		Fields: []*storagepb.FieldValue{double("open", closeValue-1), double("high", closeValue+1), double("low", closeValue-2), double("close", closeValue), double("volume", 2), double("quote_volume", 20), &storagepb.FieldValue{FieldId: "trade_num", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 3}}}},
	}
}

// TestKlineResamplePipelineE2E exercises the durable rule expansion, exact
// five-bar window, idempotent target write, source correction, and incomplete
// source behavior in one deterministic Collector-local pipeline.
func TestKlineResamplePipelineE2E(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: t.TempDir() + "/collector.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	rule := domain.TaskRule{SpaceID: "crypto", RuleID: "e2e-resample-5m", DataType: "kline_resample", Provider: "binance", MarketType: "spot", PrepareState: domain.PrepareStateReady, Enabled: true, CollectParams: `{"provider":"binance","market_type":"spot","source_dataset_id":"binance_spot_kline_1m","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_resample_5m","target_frequency":"5m","alignment":"epoch_utc"}`}
	require.NoError(t, db.TaskRules().Create(ctx, rule))
	require.NoError(t, resample.PlanRule(ctx, resampleE2ESource{}, db.TaskInstances(), rule, time.Date(2026, 8, 29, 9, 10, 0, 0, time.UTC)))
	instances, _, err := db.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: rule.SpaceID, RuleID: rule.RuleID, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, instances, 2)

	primary := &resampleE2EPrimary{rows: make(map[string]*storagepb.RowFieldValues), targets: make(map[string]*storagepb.RowFieldUpsert)}
	start := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	for _, subject := range []string{"BTC", "ETH"} {
		for i := 0; i < 5; i++ {
			row := e2eSourceRow(rule.SpaceID, "binance_spot_kline_1m", subject, start.Add(time.Duration(i)*time.Minute), float64(i+10))
			primary.rows[rowKeyText(row.Key)] = row
		}
	}
	sourceFreq, _ := resample.ParseFixedFrequency("1m")
	targetFreq, _ := resample.ParseFixedFrequency("5m")
	spec := resample.RuleSpec{RuleID: rule.RuleID, SpaceID: rule.SpaceID, SourceDatasetID: "binance_spot_kline_1m", SourceFrequency: sourceFreq, SourceSeriesTag: "venue:binance", TargetDatasetID: "spot_kline_resample_5m", TargetFrequency: targetFreq, Alignment: resample.AlignmentEpochUTC}
	result, wrote, err := (&resample.BucketStorage{Primary: primary}).ProcessBucket(ctx, spec, "BTC", start, start.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, wrote)
	require.Equal(t, float64(14), result.Close)
	require.Len(t, primary.targets, 1)
	_, wrote, err = (&resample.BucketStorage{Primary: primary}).ProcessBucket(ctx, spec, "BTC", start, start.Add(5*time.Minute))
	require.NoError(t, err)
	require.False(t, wrote)

	corrected := e2eSourceRow(rule.SpaceID, "binance_spot_kline_1m", "BTC", start.Add(4*time.Minute), 99)
	primary.rows[rowKeyText(corrected.Key)] = corrected
	_, wrote, err = (&resample.BucketStorage{Primary: primary}).ProcessBucket(ctx, spec, "BTC", start, start.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, wrote)
	require.Equal(t, 2, primary.writes)
	delete(primary.rows, rowKeyText(e2eSourceRow(rule.SpaceID, "binance_spot_kline_1m", "ETH", start.Add(3*time.Minute), 13).Key))
	_, _, err = (&resample.BucketStorage{Primary: primary}).ProcessBucket(ctx, spec, "ETH", start, start.Add(5*time.Minute))
	require.ErrorIs(t, err, resample.ErrResampleSourceIncomplete)
}
