package access

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
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

func TestServiceScansTimeSeriesDatasetWhenSubjectAndFreqAreEmpty(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})

	_, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{
		{
			Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 10)},
		},
		{
			Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "ETH-USDT", Freq: "1m", DataTime: "2026-06-15T00:01:00Z"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 20)},
		},
	}})
	require.NoError(t, err)

	rsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:30Z", EndTime: "2026-06-15T00:01:30Z"},
		Page:      &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "ETH-USDT", rsp.GetRows()[0].GetKey().GetSubjectId())
	require.Equal(t, "1m", rsp.GetRows()[0].GetKey().GetFreq())
}

func TestServiceScansTimeSeriesDatasetAcrossRoutesAndPreservesDimensions(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "split_kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})
	_, err := svc.metadata.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{NodeId: "node-2", Name: "node-2", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDevice(ctx, &pb.Device{DeviceId: "pebble-2", NodeId: "node-2", Name: "pebble-2", Engine: "pebble", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "split-kline-btc", DatasetId: "split_kline", SubjectId: "BTC-USDT", NodeId: "node-1", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "split-kline-eth", DatasetId: "split_kline", SubjectId: "ETH-USDT", NodeId: "node-2", Status: "active"})
	require.NoError(t, err)

	_, err = svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{
		{
			Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "split_kline", SubjectId: "BTC-USDT", Freq: "1m", Dimensions: map[string]string{"adj": "raw"}, DataTime: "2026-06-15T00:00:00Z"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 10)},
		},
		{
			Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "split_kline", SubjectId: "ETH-USDT", Freq: "1m", DataTime: "2026-06-15T00:01:00Z"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 20)},
		},
	}})
	require.NoError(t, err)

	rsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "split_kline"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:00Z", EndTime: "2026-06-15T00:01:00Z"},
		Page:      &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 2)
	bySubject := make(map[string]*pb.TimeSeriesRow, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		bySubject[row.GetKey().GetSubjectId()] = row
	}
	require.Contains(t, bySubject, "BTC-USDT")
	require.Contains(t, bySubject, "ETH-USDT")
	require.Equal(t, map[string]string{"adj": "raw"}, bySubject["BTC-USDT"].GetKey().GetDimensions())
}

func TestServiceScansTimeSeriesDatasetAcrossPrimaryTargets(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "routed_kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})
	_, err := svc.metadata.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{NodeId: "node-2", Name: "node-2", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDevice(ctx, &pb.Device{DeviceId: "pebble-2", NodeId: "node-2", Name: "pebble-2", Engine: "pebble", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "routed-kline-eth", DatasetId: "routed_kline", SubjectId: "ETH-USDT", NodeId: "node-2", Status: "active"})
	require.NoError(t, err)
	fake := &scanTargetsPrimaryClient{}
	svc.primary = fake

	rsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "routed_kline"}},
		Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.ElementsMatch(t, []string{"node-1", "node-2"}, fake.scannedNodes)
	require.ElementsMatch(t, []string{"BTC-USDT", "ETH-USDT"}, []string{
		rsp.GetRows()[0].GetKey().GetSubjectId(),
		rsp.GetRows()[1].GetKey().GetSubjectId(),
	})
}

func TestServiceScansTimeSeriesDatasetReadsAllPrimaryPages(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "deep_kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})
	rows := make([]*pb.TimeSeriesRow, 0, 1001)
	for idx := 0; idx < 1001; idx++ {
		rows = append(rows, &pb.TimeSeriesRow{
			Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "deep_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: fmt.Sprintf("2026-06-15T00:%02d:%02dZ", idx/60, idx%60)},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", float64(idx))},
		})
	}
	_, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: rows})
	require.NoError(t, err)

	rsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "deep_kline"}},
		Page: &pb.Page{Size: 1001},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1001)
}

func TestServiceRejectsReservedTimeSeriesAttributes(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline_reserved", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})

	rsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key:        &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline_reserved", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns:    []*pb.ColumnValue{testutil.DoubleValue("close", 10)},
		Attributes: map[string]string{timeSeriesDimensionsAttribute: `{"adj":"raw"}`},
	}}})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCurrentTimeSeriesRowsUsesInjectedTimeSeriesReader(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	reader := &fakeTimeSeriesFactReader{rows: []*pb.TimeSeriesRow{{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 10)},
	}}}
	svc.timeSeriesFactReader = reader

	rows, err := svc.currentTimeSeriesRows(ctx, []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"}})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, reader.reads)
}

func TestTimeSeriesDirtyKeyIncludesDimensions(t *testing.T) {
	raw := timeSeriesDirtyKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z", Dimensions: map[string]string{"adjust": "raw"}})
	qfq := timeSeriesDirtyKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z", Dimensions: map[string]string{"adjust": "qfq"}})
	require.NotEqual(t, raw, qfq)
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

func TestServiceScansRecordDatasetWhenRecordIDIsEmpty(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name"})

	_, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT", Version: "v1"}, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Bitcoin")}},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "ETH-USDT", Version: "v1"}, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Ethereum")}},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "ETH-USDT", Version: "v2"}, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Ethereum v2")}},
	}})
	require.NoError(t, err)

	rsp, err := svc.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{
		Keys:  []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols"}},
		Order: pb.SortOrder_SORT_ORDER_DESC,
		Page:  &pb.Page{Page: 1, Size: 3},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 3)
	require.Equal(t, uint32(3), rsp.GetPageResult().GetTotal())
	require.ElementsMatch(t, []string{"BTC-USDT:v1", "ETH-USDT:v1", "ETH-USDT:v2"}, []string{
		rsp.GetRows()[0].GetKey().GetRecordId() + ":" + rsp.GetRows()[0].GetKey().GetVersion(),
		rsp.GetRows()[1].GetKey().GetRecordId() + ":" + rsp.GetRows()[1].GetKey().GetVersion(),
		rsp.GetRows()[2].GetKey().GetRecordId() + ":" + rsp.GetRows()[2].GetKey().GetVersion(),
	})
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

func TestServiceCreateViewNormalizesDatasetIDs(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1h"}, []string{"close"})
	seedDataset(t, ctx, svc, "factor", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m", "1h", "1d"}, []string{"alpha"})

	rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_factor_view",
		Name:             "Kline Factor View",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"factor", "kline", "factor"},
		GrainKeys:        []string{"manual"},
		Engine:           "bleve",
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"kline", "factor"}, rsp.GetView().GetDatasetIds())
	require.Equal(t, []string{"subject_id", "freq", "data_time"}, rsp.GetView().GetGrainKeys())
	require.Equal(t, "duckdb", rsp.GetView().GetEngine())
}

func TestServiceRejectsInvalidDatasetAndViewIDs(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)

	for _, datasetID := range []string{"BadDataset", "bad-dataset", "bad.dataset", "dataset_id_that_is_too_long"} {
		rsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
			SpaceId:      "crypto",
			DatasetId:    datasetID,
			DataSourceId: "binance",
			Name:         "Bad Dataset",
			DataKind:     pb.DataKind_DATA_KIND_RECORD,
			Status:       "active",
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), datasetID)
	}

	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name"})
	for _, viewID := range []string{"BadView", "bad-view", "bad.view", "view_id_that_is_definitely_too_long"} {
		rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId:          "crypto",
			ViewId:           viewID,
			Name:             "Bad View",
			PrimaryDatasetId: "symbols",
			DatasetIds:       []string{"symbols"},
			Status:           "active",
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), viewID)
	}
}

func TestServiceCreateRecordViewDefaultsGrainKeys(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"name"})

	rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "symbols_view",
		Name:             "Symbols View",
		PrimaryDatasetId: "symbols",
		DatasetIds:       []string{"symbols"},
		GrainKeys:        []string{"manual"},
		Engine:           "duckdb",
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"record_id", "version"}, rsp.GetView().GetGrainKeys())
	require.Equal(t, "bleve", rsp.GetView().GetEngine())
}

func TestServiceCreateViewAllowsMixedDatasetKinds(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1h"}, []string{"close"})
	seedDataset(t, ctx, svc, "news", pb.DataKind_DATA_KIND_RECORD, nil, []string{"title"})

	rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "bad_view",
		Name:             "Bad View",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline", "news"},
		Engine:           "duckdb",
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"kline", "news"}, rsp.GetView().GetDatasetIds())
	require.Equal(t, "duckdb", rsp.GetView().GetEngine())
}

func TestServiceCreateViewAllowsMismatchedTimeSeriesFreqs(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline_1h", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1h"}, []string{"close"})
	seedDataset(t, ctx, svc, "factor_5m", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"5m"}, []string{"alpha"})

	rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "bad_freq_view",
		Name:             "Bad Freq View",
		PrimaryDatasetId: "kline_1h",
		DatasetIds:       []string{"kline_1h", "factor_5m"},
		Engine:           "duckdb",
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"kline_1h", "factor_5m"}, rsp.GetView().GetDatasetIds())
}

func TestServiceUpsertViewColumnRequiresQualifiedDatasetColumnName(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1h"}, []string{"close"})
	rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view",
		Name:             "Kline View",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	bad, err := svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())

	good, err := svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "kline.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, good.GetRetInfo().GetCode())
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
	require.NoError(t, bus.Wait(ctx))

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

func TestRecordRowMatchesStringFunctionFilters(t *testing.T) {
	row := &pb.RecordRow{Columns: []*pb.ColumnValue{
		testutil.StringValue("symbol", "BTC-USDT"),
		testutil.StringValue("note", "tradeable"),
	}}
	row.Key = &pb.RecordKey{RecordId: "record-btc", Version: "2026-06-01T00:00:00Z"}
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{
		Expr: "starts_with(symbol, $prefix)",
		Args: map[string]*pb.TypedValue{"prefix": {Value: &pb.TypedValue_StringValue{StringValue: "BTC"}}},
	}))
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{
		Expr: "record_id == $record_id",
		Args: map[string]*pb.TypedValue{"record_id": {Value: &pb.TypedValue_StringValue{StringValue: "record-btc"}}},
	}))
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{
		Expr: "version contains $version",
		Args: map[string]*pb.TypedValue{"version": {Value: &pb.TypedValue_StringValue{StringValue: "2026"}}},
	}))
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{
		Expr: "ends_with(symbol, $suffix)",
		Args: map[string]*pb.TypedValue{"suffix": {Value: &pb.TypedValue_StringValue{StringValue: "USDT"}}},
	}))
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{
		Expr: "not_contains(note, $blocked)",
		Args: map[string]*pb.TypedValue{"blocked": {Value: &pb.TypedValue_StringValue{StringValue: "test"}}},
	}))
	require.False(t, recordRowMatchesFilter(row, &pb.FilterExpr{Expr: "is_empty(note)"}))
	require.True(t, recordRowMatchesFilter(&pb.RecordRow{}, &pb.FilterExpr{Expr: "is_empty(note)"}))
	require.True(t, recordRowMatchesFilter(row, &pb.FilterExpr{Expr: "is_not_empty(note)"}))
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

func TestServiceRebuildRecordViewAggregatesMultipleRecordDatasets(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"symbol", "status"})
	seedDataset(t, ctx, svc, "profiles", pb.DataKind_DATA_KIND_RECORD, nil, []string{"sector", "description"})
	_, err := svc.metadata.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "symbol_profile_view",
		Name:             "Symbol Profile View",
		PrimaryDatasetId: "symbols",
		DatasetIds:       []string{"symbols", "profiles"},
		Engine:           "bleve",
		Status:           "active",
	})
	require.NoError(t, err)
	for _, column := range []*pb.ViewColumn{
		{ColumnName: "symbols.symbol", OriginId: "symbols.symbol", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "symbols.status", OriginId: "symbols.status", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "profiles.sector", OriginId: "profiles.sector", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "profiles.description", OriginId: "profiles.description", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	} {
		column.SpaceId = "crypto"
		column.ViewId = "symbol_profile_view"
		column.OriginType = pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN
		_, err = svc.metadata.UpsertViewColumn(ctx, column)
		require.NoError(t, err)
	}

	_, err = svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{
		{
			Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT", Version: "v1"},
			Columns: []*pb.ColumnValue{
				testutil.StringValue("symbol", "BTC-USDT"),
				testutil.StringValue("status", "active"),
			},
		},
		{
			Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "profiles", RecordId: "BTC-USDT", Version: "v1"},
			Columns: []*pb.ColumnValue{
				testutil.StringValue("sector", "crypto"),
				testutil.StringValue("description", "Bitcoin market profile"),
			},
		},
		{
			Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "ETH-USDT", Version: "v1"},
			Columns: []*pb.ColumnValue{
				testutil.StringValue("symbol", "ETH-USDT"),
				testutil.StringValue("status", "inactive"),
			},
		},
	}})
	require.NoError(t, err)

	require.NoError(t, svc.rebuildRecordView(ctx, &pb.RebuildRecordViewReq{SpaceId: "crypto", ViewId: "symbol_profile_view"}))
	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:   "crypto",
		ViewId:    "symbol_profile_view",
		TextQuery: "Bitcoin",
		Keys:      []*pb.RecordKey{{RecordId: "BTC-USDT", Version: "v1"}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "symbols", rsp.GetRows()[0].GetKey().GetDatasetId())
	require.ElementsMatch(t, []string{"symbols.symbol", "symbols.status", "profiles.sector", "profiles.description"}, recordColumnNames(rsp.GetRows()[0]))
	require.Equal(t, "Bitcoin market profile", recordColumnString(rsp.GetRows()[0], "profiles.description"))
}

func recordColumnNames(row *pb.RecordRow) []string {
	names := make([]string, 0, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		names = append(names, column.GetColumnName())
	}
	return names
}

func recordColumnString(row *pb.RecordRow, name string) string {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue().GetStringValue()
		}
	}
	return ""
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

type scanTargetsPrimaryClient struct {
	scannedNodes []string
}

type fakeTimeSeriesFactReader struct {
	rows  []*pb.TimeSeriesRow
	reads int
}

func (r *fakeTimeSeriesFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.reads++
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: r.rows}, nil
}

func (r *fakeTimeSeriesFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return r.rows, &pb.PageResult{}, nil
}

func (c *scanTargetsPrimaryClient) WriteRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	return nil
}

func (c *scanTargetsPrimaryClient) ReadRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (c *scanTargetsPrimaryClient) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	c.scannedNodes = append(c.scannedNodes, target.GetNodeId())
	subjectID := "BTC-USDT"
	if target.GetNodeId() == "node-2" {
		subjectID = "ETH-USDT"
	}
	return []*pb.PrimaryStoreRow{{
		Key: &pb.PrimaryStoreKey{
			SpaceId:   target.GetSpaceId(),
			DatasetId: target.GetDatasetId(),
			DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
			Key:       factkey.BuildTimeSeriesDataKey(subjectID, "1m", nil),
			Version:   "2026-06-15T00:00:00.000000000Z",
		},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 10)},
	}}, &pb.PageResult{Size: req.GetPage().GetSize()}, nil
}
