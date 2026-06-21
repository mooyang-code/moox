package access

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestServiceWritesAndReadsTimeSeriesRows(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close", "volume"})

	writeRsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00+08:00"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1), testutil.DoubleValue("volume", 100)},
	}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:        []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
		TimeRange:   &pb.TimeRange{StartTime: "2026-06-14T16:00:00Z", EndTime: "2026-06-14T16:00:00Z"},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
	require.Len(t, readRsp.GetRows()[0].GetColumns(), 1)
	require.Equal(t, "close", readRsp.GetRows()[0].GetColumns()[0].GetColumnName())
}

func TestServiceWritesRecordRowsWithPatchSemantics(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name", "status"})

	key := &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT", Version: "v1"}
	_, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{Key: key, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Aptos")}}}})
	require.NoError(t, err)
	_, err = svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{Key: key, Columns: []*pb.ColumnValue{testutil.StringValue("status", "active")}}}})
	require.NoError(t, err)

	readRsp, err := svc.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{key}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
	require.Len(t, readRsp.GetRows()[0].GetColumns(), 2)
}

func TestServiceReadsDefaultRecordVersionOnly(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name"})

	writeRsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"}, Columns: []*pb.ColumnValue{testutil.StringValue("name", "default")}},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT", Version: "v1"}, Columns: []*pb.ColumnValue{testutil.StringValue("name", "versioned")}},
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())
	require.Len(t, writeRsp.GetKeys(), 2)
	require.NotEmpty(t, writeRsp.GetKeys()[0].GetVersion())
	require.Equal(t, "v1", writeRsp.GetKeys()[1].GetVersion())

	readRsp, err := svc.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{writeRsp.GetKeys()[0]},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
	require.Equal(t, writeRsp.GetKeys()[0].GetVersion(), readRsp.GetRows()[0].GetKey().GetVersion())
	require.Equal(t, "default", readRsp.GetRows()[0].GetColumns()[0].GetValue().GetStringValue())
}

func TestServicePublishesRecordChangedEvent(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	svc := newAccessTestServiceWithEvents(t, bus)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name"})

	writeRsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"},
		Columns: []*pb.ColumnValue{testutil.StringValue("name", "Aptos")},
	}}})
	require.NoError(t, err)
	require.Len(t, bus.RecordEvents(), 1)
	require.Equal(t, "APT-USDT", bus.RecordEvents()[0].GetKeys()[0].GetRecordId())
	require.Equal(t, writeRsp.GetKeys()[0].GetVersion(), bus.RecordEvents()[0].GetKeys()[0].GetVersion())
}

func TestServiceTimeSeriesEventConsumerWritesActiveAndBuildingResults(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	svc := newAccessTestServiceWithEvents(t, bus)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})

	activeResult := "ts_view_crypto_kline_active_test"
	buildingResult := "ts_view_crypto_kline_building_test"
	viewColumns := []*pb.ViewColumn{{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "close_alias",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}
	_, err := svc.metadata.UpsertView(ctx, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "kline_view",
		Name:                "Kline View",
		PrimaryDatasetId:    "kline",
		DatasetIds:          []string{"kline"},
		Engine:              "duckdb",
		ViewVersion:         2,
		ActiveViewVersion:   1,
		ActiveResult:        activeResult,
		BuildingViewVersion: 2,
		BuildingResult:      buildingResult,
		BuildStatus:         "building",
		Status:              "active",
		Columns:             viewColumns,
	})
	require.NoError(t, err)
	viewStore, err := svc.viewStore()
	require.NoError(t, err)
	require.NoError(t, viewStore.CreateResultTable(ctx, activeResult, viewColumns))
	require.NoError(t, viewStore.CreateResultTable(ctx, buildingResult, viewColumns))
	require.NoError(t, svc.StartEventConsumers(ctx))

	row := &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}
	writeRsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{row}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	assertViewResultRows := func(tableName string) {
		t.Helper()
		_, rows, _, err := viewStore.QueryTimeSeriesRows(ctx, tableName, &pb.QueryTimeSeriesRowsReq{
			Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, "2026-06-15T00:00:00.000000000Z", rows[0].GetKey().GetDataTime())
		require.Len(t, rows[0].GetColumns(), 1)
		require.Equal(t, "close_alias", rows[0].GetColumns()[0].GetColumnName())
	}
	assertViewResultRows(activeResult)
	assertViewResultRows(buildingResult)
}

func TestServiceSearchRecordRowsReturnsColumnsAndFiltersKeyVersion(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "news", pb.DataKind_DATA_KIND_RECORD, nil, []string{"title", "body"})
	_, err := svc.metadata.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
		SpaceId: "crypto", DatasetId: "news", ColumnName: "title",
		OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "title",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active",
	})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertView(ctx, &pb.View{SpaceId: "crypto", ViewId: "news_view", Name: "News", PrimaryDatasetId: "news", DatasetIds: []string{"news"}, Engine: "bleve", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "news_view", ColumnName: "headline", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "news.title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "news_view", ColumnName: "body", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "news.body", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING})
	require.NoError(t, err)

	viewMeta, err := svc.metadata.GetView(ctx, "crypto", "news_view")
	require.NoError(t, err)
	resultName := "record_view_crypto_news_v2_test"
	_, err = svc.metadata.BeginViewBuild(ctx, "crypto", "news_view", viewMeta.GetViewVersion(), resultName)
	require.NoError(t, err)
	err = svc.search.IndexRecordViewRows(ctx, resultName, viewMeta.GetColumns(), []*pb.RecordRow{
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "v1"}, Columns: []*pb.ColumnValue{testutil.StringValue("headline", "same token"), testutil.StringValue("body", "old")}},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "v2"}, Columns: []*pb.ColumnValue{testutil.StringValue("headline", "same token"), testutil.StringValue("body", "new")}},
	})
	require.NoError(t, err)
	require.NoError(t, svc.metadata.CompleteViewBuild(ctx, "crypto", "news_view", viewMeta.GetViewVersion(), resultName))

	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:     "crypto",
		ViewId:      "news_view",
		Keys:        []*pb.RecordKey{{RecordId: "news-1", Version: "v2"}},
		TextQuery:   "token",
		ColumnNames: []string{"headline"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetColumns(), 1)
	require.Equal(t, "headline", rsp.GetColumns()[0].GetColumnName())
	require.Equal(t, "news", rsp.GetColumns()[0].GetDatasetId())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "v2", rsp.GetRows()[0].GetKey().GetVersion())
	require.Len(t, rsp.GetRows()[0].GetColumns(), 1)
	require.Equal(t, "headline", rsp.GetRows()[0].GetColumns()[0].GetColumnName())

	rangeRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:      "crypto",
		ViewId:       "news_view",
		Keys:         []*pb.RecordKey{{RecordId: "news-1"}},
		TextQuery:    "token",
		VersionRange: &pb.VersionRange{StartVersion: "v1", EndVersion: "v2"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rangeRsp.GetRetInfo().GetCode())
	require.Len(t, rangeRsp.GetRows(), 2)

	err = svc.search.IndexRecordViewRows(ctx, resultName, viewMeta.GetColumns(), []*pb.RecordRow{{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-time", Version: "2026-01-01T00:00:00.000000000Z"},
		Columns: []*pb.ColumnValue{testutil.StringValue("headline", "time token")},
	}})
	require.NoError(t, err)
	timeRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:   "crypto",
		ViewId:    "news_view",
		Keys:      []*pb.RecordKey{{RecordId: "news-time", Version: "2026-01-01T00:00:00Z"}},
		TextQuery: "time",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, timeRsp.GetRetInfo().GetCode())
	require.Len(t, timeRsp.GetRows(), 1)
}

func TestServiceSearchRecordRowsMissingActiveIndexReturnsError(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "news", pb.DataKind_DATA_KIND_RECORD, nil, []string{"title"})
	_, err := svc.metadata.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "news_view",
		Name:             "News",
		PrimaryDatasetId: "news",
		DatasetIds:       []string{"news"},
		Engine:           "bleve",
		ActiveResult:     "record_view_missing",
		BuildStatus:      "active",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "news_view",
		ColumnName: "title",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "news.title",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	})
	require.NoError(t, err)

	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{SpaceId: "crypto", ViewId: "news_view", TextQuery: "token"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

func TestServiceRebuildRecordViewProjectsColumnsAndCreatesEmptyIndex(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "news", pb.DataKind_DATA_KIND_RECORD, nil, []string{"title"})
	_, err := svc.metadata.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "news_view",
		Name:             "News",
		PrimaryDatasetId: "news",
		DatasetIds:       []string{"news"},
		Engine:           "bleve",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "news_view",
		ColumnName: "headline",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "news.title",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	})
	require.NoError(t, err)

	require.NoError(t, svc.rebuildRecordView(ctx, &pb.RebuildRecordViewReq{SpaceId: "crypto", ViewId: "news_view"}))
	emptyRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{SpaceId: "crypto", ViewId: "news_view", TextQuery: "token"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, emptyRsp.GetRetInfo().GetCode())
	require.Empty(t, emptyRsp.GetRows())

	writeRsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "v1"},
		Columns: []*pb.ColumnValue{testutil.StringValue("title", "same token")},
	}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	require.NoError(t, svc.rebuildRecordView(ctx, &pb.RebuildRecordViewReq{SpaceId: "crypto", ViewId: "news_view"}))
	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:     "crypto",
		ViewId:      "news_view",
		TextQuery:   "token",
		ColumnNames: []string{"headline"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Len(t, rsp.GetRows()[0].GetColumns(), 1)
	require.Equal(t, "headline", rsp.GetRows()[0].GetColumns()[0].GetColumnName())
}

func newAccessTestService(t *testing.T) *Service {
	t.Helper()
	return newAccessTestServiceWithEvents(t, eventbus.NewMemoryBus())
}

func newAccessTestServiceWithEvents(t *testing.T, events eventbus.Bus) *Service {
	t.Helper()
	return newAccessTestServiceWithRootAndEvents(t, t.TempDir(), events)
}

func newAccessTestServiceWithRootAndEvents(t *testing.T, root string, events eventbus.Bus) *Service {
	t.Helper()
	ctx := context.Background()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{Path: filepath.Join(root, "metadata.db"), SchemaPath: schemaPath(t)})
	require.NoError(t, err)
	require.NoError(t, meta.InitSchema(ctx))
	svc := NewServiceWithOptions(Options{Root: root, Metadata: meta, MetadataReader: meta, Events: events})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{NodeId: "node-1", Name: "node-1", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertDevice(ctx, &pb.Device{DeviceId: "pebble-1", NodeId: "node-1", Name: "pebble-1", Engine: "pebble", Status: "active"})
	require.NoError(t, err)
	return svc
}

func seedDataset(t *testing.T, ctx context.Context, svc *Service, datasetID string, kind pb.DataKind, freqs []string, columns []string) {
	t.Helper()
	_, err := svc.metadata.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: datasetID, DataSourceId: "binance", Name: datasetID, DataKind: kind, Freqs: freqs, Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: datasetID + "-route", DatasetId: datasetID, SubjectPattern: "*", NodeId: "node-1", Status: "active"})
	require.NoError(t, err)
	for _, name := range columns {
		valueType := pb.FieldValueType_FIELD_VALUE_TYPE_STRING
		if name == "close" || name == "volume" {
			valueType = pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		}
		_, err = svc.metadata.UpsertDatasetColumn(ctx, &pb.DatasetColumn{SpaceId: "crypto", DatasetId: datasetID, ColumnName: name, OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: name, ValueType: valueType, Status: "active"})
		require.NoError(t, err)
	}
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql")
}
