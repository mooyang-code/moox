package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestDatasetCacheHitsAfterColdPrimaryFetch(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{datasetWindowRow("BTC-USDT", period, 101)}}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	key := datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT")
	chunk, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []any{float64(101)}, chunk.Frame.Rows[0])
	require.Len(t, stub.reqs, 1)
	chunk, err = client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []any{float64(101)}, chunk.Frame.Rows[0])
	require.Len(t, stub.reqs, 1)
}

func TestDatasetCacheDeleteStillReadsCorrectly(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{datasetWindowRow("BTC-USDT", period, 101)}}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	key := datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT")
	_, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.NoError(t, client.datasetCache.DeleteWindow(context.Background(), key, period))
	stub.rows[0].Fields[0].Value = &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 202}}
	chunk, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []any{float64(202)}, chunk.Frame.Rows[0])
	require.Len(t, stub.reqs, 2)
}

func TestDatasetCacheNewOutputColumnsOpenEmptyGeneration(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{datasetWindowRow("BTC-USDT", period, 101)}}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	key := datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT")
	_, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	next := append(datasetCacheSchema(), inputcache.Column{Name: "rank", Type: "DOUBLE"})
	client.datasetCache.RegisterSchema("mdataset_a", "schema-2", next, []string{"close"})
	key.StorageSchemaID = "schema-2"
	_, err = client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.Len(t, stub.reqs, 2)
}

func TestDatasetCacheIsolatesDatasetsWithSameSchema(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{datasetWindowRow("BTC-USDT", period, 101)}}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	client.datasetCache.RegisterSchema("mdataset_b", "schema-1", datasetCacheSchema(), []string{"close"})
	_, err := client.ReadPeriodChunk(context.Background(), datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT"), period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	stub.rows = []*storagepb.TimeSeriesRow{datasetWindowRow("BTC-USDT", period, 9)}
	chunk, err := client.ReadPeriodChunk(context.Background(), datasetCacheWindowKey("mdataset_b", "schema-1", "BTC-USDT"), period, period.Add(time.Minute), 1, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []any{float64(9)}, chunk.Frame.Rows[0])
	require.Len(t, stub.reqs, 2)
}

func TestDatasetCacheMissingSourceDoesNotRetryForever(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	key := datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT")
	_, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.ErrorIs(t, err, ErrInsufficientHistory)
	require.Len(t, stub.reqs, 1)
}

func TestDatasetCachePartialFieldsAreNotHits(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "mdataset_a", SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{{
			FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}},
		}},
	}}}
	client := newDatasetCacheClient(t, stub, "mdataset_a", "schema-1", datasetCacheSchema(), []string{"close"})
	key := datasetCacheWindowKey("mdataset_a", "schema-1", "BTC-USDT")
	_, err := client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = client.ReadPeriodChunk(context.Background(), key, period, period.Add(time.Minute), 1, []string{"close"})
	require.ErrorIs(t, err, ErrInsufficientHistory)
	require.Len(t, stub.reqs, 1)
}

func datasetCacheSchema() []inputcache.Column {
	return []inputcache.Column{
		{Name: "subject_id", Type: "VARCHAR"},
		{Name: "freq", Type: "VARCHAR"},
		{Name: "data_time", Type: "TIMESTAMP_NS"},
		{Name: "series_tag", Type: "VARCHAR"},
		{Name: "close", Type: "DOUBLE"},
		{Name: "bias", Type: "DOUBLE"},
	}
}

func datasetCacheWindowKey(datasetID, schemaID, subject string) WindowKey {
	return WindowKey{SpaceID: "crypto", SourceDataset: datasetID, SubjectID: subject, Freq: "1m", StorageSchemaID: schemaID}
}

func newDatasetCacheClient(t *testing.T, stub *datasetPrimaryStub, datasetID, schemaID string, columns []inputcache.Column, base []string) *Client {
	t.Helper()
	cfg := inputcache.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1 << 20
	cfg.MinFreeBytes = 1
	cfg.RebuildKeepRows = 8
	manager, err := inputcache.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	cache := NewDatasetCache(manager)
	cache.RegisterSchema(datasetID, schemaID, columns, base)
	return (&Client{primary: stub, auth: &commonpb.AuthInfo{AppId: "moox-factor-engine"}}).WithDatasetCache(cache)
}
