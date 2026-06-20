package access

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestServiceDataAndQueryView(t *testing.T) {
	svc := newTestService(t)
	scope := &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"}

	spaceRsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "default", Name: "default"}})
	require.NoError(t, err)
	sourceRsp, err := svc.CreateDataSource(context.Background(), &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "default", DataSourceId: "binance", Name: "binance", Kind: "exchange"}})
	require.NoError(t, err)
	datasetRsp, err := svc.CreateDataSet(context.Background(), &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: "default", DatasetId: "binance_spot_kline_1m", DataSourceId: sourceRsp.GetDataSource().GetDataSourceId(), Name: "binance_spot_kline_1m", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}}})
	require.NoError(t, err)
	_, err = svc.UpsertDataSetColumn(context.Background(), &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: "default", DatasetId: datasetRsp.GetDataset().GetDatasetId(), ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	bindTestSubjects(t, svc, "default", datasetRsp.GetDataset().GetDatasetId(), "APT-USDT")
	seedRoute(t, svc, "default", datasetRsp.GetDataset().GetDatasetId())

	writeRsp, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_APPEND,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:00:00"},
			Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3.14}}}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(context.Background(), &pb.ReadRowsReq{
		Scope:       scope,
		ReadMode:    pb.ReadMode_READ_MODE_RANGE,
		TimeRange:   &pb.TimeRange{StartTime: "2026-01-01 00:00:00"},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)

	viewRsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{
		View: &pb.View{
			ViewId:           "kline_close_view",
			SpaceId:          spaceRsp.GetSpace().GetSpaceId(),
			Name:             "kline_close_view",
			PrimaryDatasetId: datasetRsp.GetDataset().GetDatasetId(),
			DatasetIds:       []string{datasetRsp.GetDataset().GetDatasetId()},
		},
	})
	require.NoError(t, err)
	refreshMetadataCacheForTest(t, svc)

	queryRsp, err := svc.QueryView(context.Background(), &pb.QueryViewReq{
		SpaceId:     "default",
		ViewId:      viewRsp.GetView().GetViewId(),
		SubjectIds:  []string{"APT-USDT"},
		QueryTime:   &pb.QueryTime{TimeRange: &pb.TimeRange{StartTime: "2026-01-01 00:00:00"}},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, queryRsp.GetRetInfo().GetCode())
}

func TestQueryViewReturnsViewNotFoundWhenViewHasNoActiveResult(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto", Name: "crypto"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())

	sourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode())

	datasetRsp, err := svc.CreateDataSet(ctx, &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())

	_, err = svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: datasetRsp.GetDataset().GetDatasetId(),
		DatasetIds:       []string{datasetRsp.GetDataset().GetDatasetId()},
		QueryWindow:      "30d",
		Status:           "active",
	}})
	require.NoError(t, err)
	refreshMetadataCacheForTest(t, svc)

	rsp, err := svc.QueryView(ctx, &pb.QueryViewReq{SpaceId: "crypto", ViewId: "kline_view"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestQueryViewReadsActiveDuckDBResult(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline", []string{"close"})

	viewRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view_active",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		ActiveResult:     "view_result_crypto_kline_active",
		Status:           "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, viewRsp.GetRetInfo().GetCode())
	refreshMetadataCacheForTest(t, svc)

	store, err := svc.viewStore()
	require.NoError(t, err)
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_active", []*pb.ViewColumn{{
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_active", []*pb.QueryViewRow{{
		SubjectId: "APT-USDT",
		DataTime:  "2026-06-15T00:00:00+08:00",
		Values:    []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}}))

	rsp, err := svc.QueryView(ctx, &pb.QueryViewReq{SpaceId: "crypto", ViewId: "kline_view_active", SubjectIds: []string{"APT-USDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", rsp.GetRows()[0].GetSubjectId())
}

func TestReadRowsUsesResolvedPrimaryRoute(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline", []string{"close"})

	expectedRows := []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{stringColumn("close", "8.1")},
	}}
	svc.primary = &fakeReadPrimary{rows: expectedRows}

	rsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{
		Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
		ReadMode: pb.ReadMode_READ_MODE_RANGE,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "8.1", rsp.GetRows()[0].GetColumns()[0].GetValue().GetStringValue())
}

func TestReadRowsClassifiesPrimaryFailureAsInnerError(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline_read_failure", []string{"close"})
	svc.primary = failingPrimary{}

	rsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{
		Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline_read_failure", SubjectId: "APT-USDT", Freq: "1m"},
		ReadMode: pb.ReadMode_READ_MODE_RANGE,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

func TestSearchRowsWithoutTextUsesSearchService(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols", []string{"symbol", "status"})

	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
			Columns: []*pb.ColumnValue{
				stringColumn("symbol", "APTUSDT"),
				stringColumn("status", "active"),
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())
	svc.WaitForIndex()
	svc.primary = failingPrimary{}

	rsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{
		SpaceId:   "crypto",
		DatasetId: "symbols",
		Filters: []*pb.FilterExpr{{
			Expr: "status == $status",
			Args: map[string]*pb.TypedValue{
				"status": {Value: &pb.TypedValue_StringValue{StringValue: "active"}},
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", rsp.GetRows()[0].GetKey().GetScope().GetSubjectId())
}

func TestSearchIndexUsesMergedPrimaryRowAfterPartialUpdate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_patch", []string{"note", "status"})

	rowKey := &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_patch", SubjectId: "APT-USDT"}, RowId: "APT-USDT"}
	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key: rowKey,
			Columns: []*pb.ColumnValue{
				stringColumn("note", "alpha searchable note"),
				stringColumn("status", "active"),
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	writeRsp, err = svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key:     rowKey,
			Columns: []*pb.ColumnValue{stringColumn("status", "paused")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())
	svc.WaitForIndex()

	rsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{
		SpaceId:   "crypto",
		DatasetId: "symbols_patch",
		TextQuery: "alpha",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	note, ok := rowColumnValue(rsp.GetRows()[0], "note")
	require.True(t, ok)
	require.Equal(t, "alpha searchable note", note.GetStringValue())
	status, ok := rowColumnValue(rsp.GetRows()[0], "status")
	require.True(t, ok)
	require.Equal(t, "paused", status.GetStringValue())
}

func TestSearchConsumerLoadsCurrentRowsThroughAccessReader(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_reader", []string{"note", "status"})
	rowKey := &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_reader", SubjectId: "APT-USDT"}, RowId: "APT-USDT"}
	reader := &recordingAccessReader{rows: []*pb.DataRow{{
		Key: rowKey,
		Columns: []*pb.ColumnValue{
			stringColumn("note", "alpha via access reader"),
			stringColumn("status", "active"),
		},
	}}}
	svc.factReader = reader
	svc.primary = failingPrimary{}

	err := svc.handleRowsChangedForSearch(ctx, &pb.DataRowsChangedEvent{
		EventId: "evt-access-reader",
		Scope:   rowKey.GetScope(),
		Rows: []*pb.DataRow{{
			Key:     rowKey,
			Columns: []*pb.ColumnValue{stringColumn("status", "active")},
		}},
	})
	require.NoError(t, err)
	svc.WaitForIndex()
	require.Equal(t, 1, reader.calls)
	require.Len(t, reader.requests, 1)

	rsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{
		SpaceId:   "crypto",
		DatasetId: "symbols_reader",
		TextQuery: "alpha",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
}

func TestSearchConsumerReturnsBeforeAccessReadCompletesByDefault(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_async_event", []string{"note"})
	rowKey := &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_async_event", SubjectId: "APT-USDT"}, RowId: "APT-USDT"}
	reader := &blockingAccessReader{
		release: make(chan struct{}),
		rows: []*pb.DataRow{{
			Key:     rowKey,
			Columns: []*pb.ColumnValue{stringColumn("note", "async event note")},
		}},
	}
	svc.factReader = reader
	svc.primary = failingPrimary{}

	done := make(chan error, 1)
	go func() {
		done <- svc.handleRowsChangedForSearch(ctx, &pb.DataRowsChangedEvent{
			EventId: "evt-async-default",
			Scope:   rowKey.GetScope(),
			Rows: []*pb.DataRow{{
				Key:     rowKey,
				Columns: []*pb.ColumnValue{stringColumn("note", "patch")},
			}},
		})
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		close(reader.release)
		<-done
		t.Fatal("search consumer blocked on Access ReadRows; rows_changed handling must be asynchronous by default")
	}
	close(reader.release)
	svc.WaitForIndex()
	require.Equal(t, 1, reader.calls)

	rsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{
		SpaceId:   "crypto",
		DatasetId: "symbols_async_event",
		TextQuery: "async",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
}

func TestStartEventConsumersReturnsSubscribeError(t *testing.T) {
	ctx := context.Background()
	bus := &failingSubscribeRowsChangedBus{err: errText("subscribe failed")}
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: defaultSchemaPath(), Events: bus})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	err := svc.StartEventConsumers(ctx)

	require.ErrorContains(t, err, "subscribe failed")
}

func TestRebuildSearchIndexIndexesHistoricalRowsThroughAccess(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: defaultSchemaPath(), Events: &recordingRowsChangedBus{}})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	seedStringDataset(t, svc, "crypto", "symbols_rebuild", []string{"note"})
	_, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", Name: "APT-USDT"}})
	require.NoError(t, err)
	bindRsp, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{
		SpaceId:   "crypto",
		DatasetId: "symbols_rebuild",
		SubjectId: "APT-USDT",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, bindRsp.GetRetInfo().GetCode(), bindRsp.GetRetInfo().GetMsg())
	refreshMetadataCacheForTest(t, svc)
	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key: &pb.DataKey{
				Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_rebuild", SubjectId: "APT-USDT"},
				RowId: "APT-USDT",
			},
			Columns: []*pb.ColumnValue{stringColumn("note", "alpha historical note")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	searchRsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{SpaceId: "crypto", DatasetId: "symbols_rebuild", TextQuery: "alpha"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Empty(t, searchRsp.GetRows())

	rebuildRsp, err := svc.RebuildSearchIndex(ctx, &pb.RebuildSearchIndexReq{SpaceId: "crypto", DatasetId: "symbols_rebuild", SubjectIds: []string{"APT-USDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rebuildRsp.GetRetInfo().GetCode(), rebuildRsp.GetRetInfo().GetMsg())
	require.Zero(t, rebuildRsp.GetIndexedRows())
	svc.WaitForIndex()

	searchRsp, err = svc.SearchRows(ctx, &pb.SearchRowsReq{SpaceId: "crypto", DatasetId: "symbols_rebuild", TextQuery: "alpha"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Len(t, searchRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", searchRsp.GetRows()[0].GetKey().GetScope().GetSubjectId())
}

func TestRebuildSearchIndexReturnsBeforeReadCompletes(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_async_rebuild", []string{"note"})
	_, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", Name: "APT-USDT"}})
	require.NoError(t, err)
	bindRsp, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{
		SpaceId:   "crypto",
		DatasetId: "symbols_async_rebuild",
		SubjectId: "APT-USDT",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, bindRsp.GetRetInfo().GetCode(), bindRsp.GetRetInfo().GetMsg())
	refreshMetadataCacheForTest(t, svc)
	primary := &blockingReadPrimary{
		release: make(chan struct{}),
		rows: []*pb.DataRow{{
			Key: &pb.DataKey{
				Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_async_rebuild", SubjectId: "APT-USDT"},
				RowId: "APT-USDT",
			},
			Columns: []*pb.ColumnValue{stringColumn("note", "async rebuild note")},
		}},
	}
	svc.primary = primary

	done := make(chan *pb.RebuildSearchIndexRsp, 1)
	go func() {
		rsp, err := svc.RebuildSearchIndex(ctx, &pb.RebuildSearchIndexReq{
			SpaceId:    "crypto",
			DatasetId:  "symbols_async_rebuild",
			SubjectIds: []string{"APT-USDT"},
		})
		require.NoError(t, err)
		done <- rsp
	}()

	var rsp *pb.RebuildSearchIndexRsp
	select {
	case rsp = <-done:
	case <-time.After(100 * time.Millisecond):
		close(primary.release)
		rsp = <-done
		t.Fatalf("RebuildSearchIndex blocked on data scan; rsp=%v", rsp.GetRetInfo().GetMsg())
	}
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	field := rsp.ProtoReflect().Descriptor().Fields().ByName("rebuild_id")
	require.NotNil(t, field)
	require.NotEmpty(t, rsp.ProtoReflect().Get(field).String())
	close(primary.release)
	svc.WaitForIndex()
	searchRsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{SpaceId: "crypto", DatasetId: "symbols_async_rebuild", TextQuery: "async"})
	require.NoError(t, err)
	require.Len(t, searchRsp.GetRows(), 1)
}

func TestRebuildSearchIndexRejectsUnboundSubject(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_unbound_rebuild", []string{"note"})

	rsp, err := svc.RebuildSearchIndex(ctx, &pb.RebuildSearchIndexReq{
		SpaceId:    "crypto",
		DatasetId:  "symbols_unbound_rebuild",
		SubjectIds: []string{"UNBOUND-USDT"},
	})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "not bound")
}

func TestWriteRowsRejectsUnboundSubject(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_unbound_write", []string{"symbol"})

	rsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_unbound_write", SubjectId: "UNBOUND-USDT"}, RowId: "UNBOUND-USDT"},
			Columns: []*pb.ColumnValue{stringColumn("symbol", "UNBOUNDUSDT")},
		}},
	})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "not bound")
}

func TestSearchRowsReturnsRequestedPageBeyondDefaultSearchLimit(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_large", []string{"symbol"})

	const rowCount = 1100
	rows := make([]*pb.DataRow, 0, rowCount)
	subjectIDs := make([]string, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		subjectID := fmt.Sprintf("SYM-%04d", i)
		subjectIDs = append(subjectIDs, subjectID)
		rows = append(rows, &pb.DataRow{
			Key: &pb.DataKey{
				Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_large", SubjectId: subjectID},
				RowId: subjectID,
			},
			Columns: []*pb.ColumnValue{stringColumn("symbol", subjectID)},
		})
	}
	bindTestSubjects(t, svc, "crypto", "symbols_large", subjectIDs...)
	refreshMetadataCacheForTest(t, svc)
	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{WriteMode: pb.WriteMode_WRITE_MODE_UPSERT, Rows: rows})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())
	svc.WaitForIndex()

	searchRsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{
		SpaceId:   "crypto",
		DatasetId: "symbols_large",
		Page:      &pb.Page{Page: 1, Size: rowCount},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Len(t, searchRsp.GetRows(), rowCount)
	require.Equal(t, uint64(rowCount), searchRsp.GetPageResult().GetTotal())
}

func TestWriteRowsDoesNotFailWhenSearchIndexUnavailable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	badBlevePath := filepath.Join(root, "bleve-file")
	require.NoError(t, os.WriteFile(badBlevePath, []byte("not a directory"), 0o644))
	reporter := &derivedErrorRecorder{}
	svc := NewServiceWithOptions(Options{Root: root, InitSchemaPath: defaultSchemaPath(), BlevePath: badBlevePath, DerivedErrors: reporter.Record})
	require.NoError(t, svc.StartEventConsumers(ctx))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	seedStringDataset(t, svc, "crypto", "symbols", []string{"symbol"})

	rsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
			Columns: []*pb.ColumnValue{stringColumn("symbol", "APTUSDT")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}, ReadMode: pb.ReadMode_READ_MODE_RANGE})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
	svc.WaitForIndex()
	require.True(t, reporter.Contains("search_index"), reporter.stages)
}

func TestWriteRowsDoesNotFailWhenRowsChangedEventPublishFails(t *testing.T) {
	ctx := context.Background()
	reporter := &derivedErrorRecorder{}
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: defaultSchemaPath(), Events: failingEventBus{}, DerivedErrors: reporter.Record})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	seedStringDataset(t, svc, "crypto", "symbols", []string{"symbol"})

	rsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
			Columns: []*pb.ColumnValue{stringColumn("symbol", "APTUSDT")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}, ReadMode: pb.ReadMode_READ_MODE_RANGE})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
	require.True(t, reporter.Contains("rows_changed_event"), reporter.stages)
}

func TestWriteRowsDoesNotRollbackSuccessfulPrimaryTargetWhenAnotherTargetFails(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "symbols_multi_target", []string{"symbol"})
	seedExactRoute(t, svc, "crypto", "symbols_multi_target", "APT-USDT", "node_symbols_multi_target_apt")
	seedExactRoute(t, svc, "crypto", "symbols_multi_target", "AR-USDT", "node_symbols_multi_target_ar")
	svc.primary = &failSecondWritePrimary{}

	rsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{
			{
				Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_multi_target", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
				Columns: []*pb.ColumnValue{stringColumn("symbol", "APTUSDT")},
			},
			{
				Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_multi_target", SubjectId: "AR-USDT"}, RowId: "AR-USDT"},
				Columns: []*pb.ColumnValue{stringColumn("symbol", "ARUSDT")},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Equal(t, 2, svc.primary.(*failSecondWritePrimary).calls)
	require.Len(t, svc.primary.(*failSecondWritePrimary).accepted, 1)
}

func TestWriteRowsPublishesRowsChangedForSuccessfulTargetsWhenLaterTargetFails(t *testing.T) {
	ctx := context.Background()
	bus := &recordingRowsChangedBus{}
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: defaultSchemaPath(), Events: bus})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	seedStringDataset(t, svc, "crypto", "symbols_partial_event", []string{"symbol"})
	seedExactRoute(t, svc, "crypto", "symbols_partial_event", "APT-USDT", "node_symbols_partial_event_apt")
	seedExactRoute(t, svc, "crypto", "symbols_partial_event", "AR-USDT", "node_symbols_partial_event_ar")
	svc.primary = &failSecondWritePrimary{}

	rsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{
			{
				Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_partial_event", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
				Columns: []*pb.ColumnValue{stringColumn("symbol", "APTUSDT")},
			},
			{
				Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols_partial_event", SubjectId: "AR-USDT"}, RowId: "AR-USDT"},
				Columns: []*pb.ColumnValue{stringColumn("symbol", "ARUSDT")},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Len(t, bus.events, 1)
	require.Len(t, bus.events[0].GetRows(), 1)
	require.Equal(t, "APT-USDT", bus.events[0].GetRows()[0].GetKey().GetScope().GetSubjectId())
}

func TestInitViewBuilderAllowsTimerToBuildPendingViews(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline", []string{"close"})
	_, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"}})
	require.NoError(t, err)
	_, err = svc.WriteRows(ctx, &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
				DataTime: "2026-06-15T00:00:00Z",
			},
			Columns: []*pb.ColumnValue{stringColumn("close", "8.1")},
		}},
	})
	require.NoError(t, err)
	_, err = svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		QueryWindow:      "30d",
		Status:           "active",
	}})
	require.NoError(t, err)
	_, err = svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	}})
	require.NoError(t, err)

	require.NoError(t, svc.InitViewBuilder())
	t.Cleanup(func() { view.SetDefaultBuilder(nil) })
	require.NoError(t, view.HandleSchedule(ctx, "space_id=crypto"))

	view, err := svc.metadata.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "active", view.GetBuildStatus())
	require.NotEmpty(t, view.GetActiveResult())
}

func TestInitViewBuilderReadsFactsThroughPrimaryClient(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline", []string{"close"})
	_, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"}})
	require.NoError(t, err)
	svc.primary = &fakeReadPrimary{rows: []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{stringColumn("close", "8.1")},
	}}}
	_, err = svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view_remote_facts",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		QueryWindow:      "30d",
		Status:           "active",
	}})
	require.NoError(t, err)
	_, err = svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view_remote_facts",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	}})
	require.NoError(t, err)

	require.NoError(t, svc.InitViewBuilder())
	t.Cleanup(func() { view.SetDefaultBuilder(nil) })
	require.NoError(t, view.HandleSchedule(ctx, "space_id=crypto"))
	refreshMetadataCacheForTest(t, svc)

	rsp, err := svc.QueryView(ctx, &pb.QueryViewReq{SpaceId: "crypto", ViewId: "kline_view_remote_facts", SubjectIds: []string{"APT-USDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetRows(), 1)
	require.Equal(t, "8.1", rsp.GetRows()[0].GetValues()[0].GetValue().GetStringValue())
}

type fakeReadPrimary struct {
	rows []*pb.DataRow
}

func (f *fakeReadPrimary) WriteRows(ctx context.Context, target *pb.PrimaryTarget, rows []*pb.DataRow, mode pb.WriteMode) error {
	return nil
}

func (f *fakeReadPrimary) ReadRows(ctx context.Context, target *pb.PrimaryTarget, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	reqScope := req.GetScope()
	var rows []*pb.DataRow
	for _, row := range f.rows {
		rowScope := row.GetKey().GetScope()
		if reqScope.GetSpaceId() != "" && rowScope.GetSpaceId() != reqScope.GetSpaceId() {
			continue
		}
		if reqScope.GetDatasetId() != "" && rowScope.GetDatasetId() != reqScope.GetDatasetId() {
			continue
		}
		if reqScope.GetSubjectId() != "" && rowScope.GetSubjectId() != reqScope.GetSubjectId() {
			continue
		}
		if reqScope.GetFreq() != "" && rowScope.GetFreq() != reqScope.GetFreq() {
			continue
		}
		rows = append(rows, row)
	}
	return rows, &pb.PageResult{Page: 1, Size: uint32(len(rows)), Total: uint64(len(rows))}, nil
}

type failingPrimary struct{}

func (failingPrimary) WriteRows(context.Context, *pb.PrimaryTarget, []*pb.DataRow, pb.WriteMode) error {
	return nil
}

func (failingPrimary) ReadRows(context.Context, *pb.PrimaryTarget, *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	return nil, nil, errText("primary should not be used")
}

type blockingReadPrimary struct {
	release chan struct{}
	rows    []*pb.DataRow
}

func (p *blockingReadPrimary) WriteRows(context.Context, *pb.PrimaryTarget, []*pb.DataRow, pb.WriteMode) error {
	return nil
}

func (p *blockingReadPrimary) ReadRows(context.Context, *pb.PrimaryTarget, *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	<-p.release
	return p.rows, &pb.PageResult{Page: 1, Size: uint32(len(p.rows)), Total: uint64(len(p.rows))}, nil
}

type failSecondWritePrimary struct {
	calls    int
	accepted []*pb.DataRow
}

func (p *failSecondWritePrimary) WriteRows(_ context.Context, _ *pb.PrimaryTarget, rows []*pb.DataRow, _ pb.WriteMode) error {
	p.calls++
	if p.calls == 2 {
		return errText("second primary target failed")
	}
	p.accepted = append(p.accepted, rows...)
	return nil
}

func (p *failSecondWritePrimary) ReadRows(context.Context, *pb.PrimaryTarget, *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	return nil, nil, errText("read is not used")
}

type failingEventBus struct{}

func (failingEventBus) PublishRowsChanged(context.Context, *pb.DataRowsChangedEvent) error {
	return errText("eventbus unavailable")
}

type failingSubscribeRowsChangedBus struct {
	err error
}

func (b *failingSubscribeRowsChangedBus) PublishRowsChanged(context.Context, *pb.DataRowsChangedEvent) error {
	return nil
}

func (b *failingSubscribeRowsChangedBus) SubscribeRowsChanged(context.Context, eventbus.RowsChangedHandler) (eventbus.Subscription, error) {
	return nil, b.err
}

type recordingRowsChangedBus struct {
	events []*pb.DataRowsChangedEvent
}

func (b *recordingRowsChangedBus) PublishRowsChanged(_ context.Context, event *pb.DataRowsChangedEvent) error {
	b.events = append(b.events, event)
	return nil
}

type recordingAccessReader struct {
	calls    int
	requests []*pb.ReadRowsReq
	rows     []*pb.DataRow
}

func (r *recordingAccessReader) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	_ = ctx
	r.calls++
	r.requests = append(r.requests, req)
	return &pb.ReadRowsRsp{
		RetInfo:    response.Success("success"),
		Rows:       r.rows,
		PageResult: &pb.PageResult{Page: 1, Size: uint32(len(r.rows)), Total: uint64(len(r.rows))},
	}, nil
}

type blockingAccessReader struct {
	release chan struct{}
	calls   int
	rows    []*pb.DataRow
}

func (r *blockingAccessReader) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	_ = ctx
	_ = req
	r.calls++
	<-r.release
	return &pb.ReadRowsRsp{
		RetInfo:    response.Success("success"),
		Rows:       r.rows,
		PageResult: &pb.PageResult{Page: 1, Size: uint32(len(r.rows)), Total: uint64(len(r.rows))},
	}, nil
}

type derivedErrorRecorder struct {
	stages []string
	errs   []error
}

func (r *derivedErrorRecorder) Record(ctx context.Context, stage string, err error) {
	_ = ctx
	r.stages = append(r.stages, stage)
	r.errs = append(r.errs, err)
}

func (r *derivedErrorRecorder) Contains(stage string) bool {
	for i, item := range r.stages {
		if item == stage && i < len(r.errs) && r.errs[i] != nil {
			return true
		}
	}
	return false
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	return newTestServiceWithRoot(t, t.TempDir())
}

func newTestServiceWithRoot(t *testing.T, root string) *Service {
	t.Helper()
	svc := NewServiceWithOptions(Options{Root: root, InitSchemaPath: defaultSchemaPath()})
	require.NoError(t, svc.StartEventConsumers(context.Background()))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func TestNewServiceWithOptionsUsesSnapshotMetadataCacheByDefault(t *testing.T) {
	svc := newTestService(t)
	require.NotNil(t, svc.metadata)
	require.NotNil(t, svc.metadataCache)
	cacheReader, ok := svc.metadataReader.(*metacache.Store)
	require.True(t, ok)
	require.Same(t, svc.metadataCache, cacheReader)
	require.NoError(t, svc.metadataCache.Refresh(context.Background()))
}

func TestServiceWritesAndReadsTimeSeriesRows(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDataset(t, svc, "crypto", "kline_ts_api", []string{"close"})

	writeRsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.TimeSeriesRow{
			{
				Key: &pb.TimeSeriesKey{
					SpaceId:   "crypto",
					DatasetId: "kline_ts_api",
					SubjectId: "APT-USDT",
					Freq:      "1m",
					DataTime:  "2026-06-15T00:00:00+08:00",
				},
				Columns: []*pb.ColumnValue{stringColumn("close", "8.1")},
			},
			{
				Key: &pb.TimeSeriesKey{
					SpaceId:   "crypto",
					DatasetId: "kline_ts_api",
					SubjectId: "APT-USDT",
					Freq:      "1m",
					DataTime:  "2026-06-15T00:01:00+08:00",
				},
				Columns: []*pb.ColumnValue{stringColumn("close", "8.2")},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode(), writeRsp.GetRetInfo().GetMsg())

	readRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "kline_ts_api",
			SubjectId: "APT-USDT",
			Freq:      "1m",
		}},
		TimeRange: &pb.TimeRange{
			StartTime: "2026-06-14T16:00:00Z",
			EndTime:   "2026-06-14T16:00:00Z",
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode(), readRsp.GetRetInfo().GetMsg())
	require.Len(t, readRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", readRsp.GetRows()[0].GetKey().GetSubjectId())
	require.Equal(t, "2026-06-15T00:00:00+08:00", readRsp.GetRows()[0].GetKey().GetDataTime())

	latestRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "kline_ts_api",
			SubjectId: "APT-USDT",
			Freq:      "1m",
		}},
		Order: pb.SortOrder_SORT_ORDER_DESC,
		Page:  &pb.Page{Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, latestRsp.GetRetInfo().GetCode(), latestRsp.GetRetInfo().GetMsg())
	require.Len(t, latestRsp.GetRows(), 1)
	require.Equal(t, "2026-06-15T00:01:00+08:00", latestRsp.GetRows()[0].GetKey().GetDataTime())

	invalidRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "kline_ts_api",
			SubjectId: "APT-USDT",
			Freq:      "1m",
		}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15 00:00:00"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, invalidRsp.GetRetInfo().GetCode())
}

func TestWriteTimeSeriesRowsDoesNotRequireDataSetSubjectBinding(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDatasetWithoutSubjects(t, svc, "crypto", "kline_unbound_subject", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"close"})

	writeRsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.TimeSeriesRow{{
			Key: &pb.TimeSeriesKey{
				SpaceId:   "crypto",
				DatasetId: "kline_unbound_subject",
				SubjectId: "NOT-BOUND-USDT",
				Freq:      "1m",
				DataTime:  "2026-06-15T00:00:00Z",
			},
			Columns: []*pb.ColumnValue{stringColumn("close", "8.1")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode(), writeRsp.GetRetInfo().GetMsg())

	readRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "kline_unbound_subject",
			SubjectId: "NOT-BOUND-USDT",
			Freq:      "1m",
		}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:00Z", EndTime: "2026-06-15T00:00:00Z"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode(), readRsp.GetRetInfo().GetMsg())
	require.Len(t, readRsp.GetRows(), 1)
	require.Equal(t, "NOT-BOUND-USDT", readRsp.GetRows()[0].GetKey().GetSubjectId())
}

func TestServiceWritesAndReadsObjectRowsByVersionRange(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDatasetWithoutSubjects(t, svc, "crypto", "objects_api", pb.DataKind_DATA_KIND_OBJECT, []string{"status"})

	writeRsp, err := svc.WriteObjectRows(ctx, &pb.WriteObjectRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.ObjectRow{
			{
				Key:     &pb.ObjectKey{SpaceId: "crypto", DatasetId: "objects_api", ObjectId: "APT-USDT", Version: "v1"},
				Columns: []*pb.ColumnValue{stringColumn("status", "draft")},
			},
			{
				Key:     &pb.ObjectKey{SpaceId: "crypto", DatasetId: "objects_api", ObjectId: "APT-USDT", Version: "v2"},
				Columns: []*pb.ColumnValue{stringColumn("status", "active")},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode(), writeRsp.GetRetInfo().GetMsg())
	svc.WaitForIndex()

	searchRsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{SpaceId: "crypto", DatasetId: "objects_api", TextQuery: "active"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode(), searchRsp.GetRetInfo().GetMsg())
	require.Len(t, searchRsp.GetRows(), 1)
	require.Equal(t, "v2", searchRsp.GetRows()[0].GetKey().GetDataTime())

	readRsp, err := svc.ReadObjectRows(ctx, &pb.ReadObjectRowsReq{
		Keys: []*pb.ObjectKey{{
			SpaceId:   "crypto",
			DatasetId: "objects_api",
			ObjectId:  "APT-USDT",
		}},
		VersionRange: &pb.VersionRange{StartVersion: "v2", EndVersion: "v2"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode(), readRsp.GetRetInfo().GetMsg())
	require.Len(t, readRsp.GetRows(), 1)
	require.Equal(t, "v2", readRsp.GetRows()[0].GetKey().GetVersion())
	require.Equal(t, "active", readRsp.GetRows()[0].GetColumns()[0].GetValue().GetStringValue())

	latestRsp, err := svc.ReadObjectRows(ctx, &pb.ReadObjectRowsReq{
		Keys: []*pb.ObjectKey{{
			SpaceId:   "crypto",
			DatasetId: "objects_api",
			ObjectId:  "APT-USDT",
		}},
		Order: pb.SortOrder_SORT_ORDER_DESC,
		Page:  &pb.Page{Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, latestRsp.GetRetInfo().GetCode(), latestRsp.GetRetInfo().GetMsg())
	require.Len(t, latestRsp.GetRows(), 1)
	require.Equal(t, "v2", latestRsp.GetRows()[0].GetKey().GetVersion())

	allObjectsRsp, err := svc.ReadObjectRows(ctx, &pb.ReadObjectRowsReq{
		Keys: []*pb.ObjectKey{{
			SpaceId:   "crypto",
			DatasetId: "objects_api",
			ObjectId:  "APT-USDT",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, allObjectsRsp.GetRetInfo().GetCode(), allObjectsRsp.GetRetInfo().GetMsg())
	require.Len(t, allObjectsRsp.GetRows(), 2)
}

func TestWriteObjectRowsDoesNotBindObjectsToDataSet(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedStringDatasetWithoutSubjects(t, svc, "crypto", "objects_no_auto_bind", pb.DataKind_DATA_KIND_OBJECT, []string{"status"})

	writeRsp, err := svc.WriteObjectRows(ctx, &pb.WriteObjectRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.ObjectRow{{
			Key:     &pb.ObjectKey{SpaceId: "crypto", DatasetId: "objects_no_auto_bind", ObjectId: "OBJ-001", Version: "v1"},
			Columns: []*pb.ColumnValue{stringColumn("status", "active")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode(), writeRsp.GetRetInfo().GetMsg())

	subjectsRsp, err := svc.ListDataSetSubjects(ctx, &pb.ListDataSetSubjectsReq{
		SpaceId:   "crypto",
		DatasetId: "objects_no_auto_bind",
		SubjectId: "OBJ-001",
		Page:      &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, subjectsRsp.GetRetInfo().GetCode(), subjectsRsp.GetRetInfo().GetMsg())
	require.Empty(t, subjectsRsp.GetDatasetSubjects())

	readRsp, err := svc.ReadObjectRows(ctx, &pb.ReadObjectRowsReq{
		Keys: []*pb.ObjectKey{{SpaceId: "crypto", DatasetId: "objects_no_auto_bind", ObjectId: "OBJ-001"}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode(), readRsp.GetRetInfo().GetMsg())
	require.Len(t, readRsp.GetRows(), 1)
	require.Equal(t, "v1", readRsp.GetRows()[0].GetKey().GetVersion())
}

func TestCloseKeepsSharedViewStoreOpenForOtherServices(t *testing.T) {
	root := t.TempDir()
	svc1 := NewServiceWithOptions(Options{Root: root, InitSchemaPath: defaultSchemaPath()})
	svc2 := NewServiceWithOptions(Options{Root: root, InitSchemaPath: defaultSchemaPath()})
	t.Cleanup(func() { _ = svc2.Close() })

	store1, err := svc1.viewStore()
	require.NoError(t, err)
	store2, err := svc2.viewStore()
	require.NoError(t, err)
	require.Same(t, store1, store2)

	require.NoError(t, svc1.Close())
	storeAfterClose, err := svc2.viewStore()
	require.NoError(t, err)
	require.Same(t, store2, storeAfterClose)
}

func TestServiceMetadataUsesNewModel(t *testing.T) {
	svc := newTestService(t)

	spaceRsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{Name: "research"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())
	require.NotEmpty(t, spaceRsp.GetSpace().GetSpaceId())

	sourceRsp, err := svc.CreateDataSource(context.Background(), &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: spaceRsp.GetSpace().GetSpaceId(), Name: "binance", Kind: "exchange", Market: "crypto"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode())

	subjectRsp, err := svc.UpsertSubject(context.Background(), &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: spaceRsp.GetSpace().GetSpaceId(), SubjectId: "APT-USDT", Name: "APT-USDT", SubjectType: "crypto_pair"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, subjectRsp.GetRetInfo().GetCode())

	symbolRsp, err := svc.UpsertSubjectSymbol(context.Background(), &pb.UpsertSubjectSymbolReq{SubjectSymbol: &pb.SubjectSymbol{SpaceId: spaceRsp.GetSpace().GetSpaceId(), SubjectId: subjectRsp.GetSubject().GetSubjectId(), DataSourceId: sourceRsp.GetDataSource().GetDataSourceId(), ExternalSymbol: "APTUSDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, symbolRsp.GetRetInfo().GetCode())

	datasetRsp, err := svc.CreateDataSet(context.Background(), &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DataSourceId: sourceRsp.GetDataSource().GetDataSourceId(), Name: "binance_spot_kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m", "1h"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())

	viewRsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{View: &pb.View{SpaceId: spaceRsp.GetSpace().GetSpaceId(), Name: "kline_factor_view", PrimaryDatasetId: datasetRsp.GetDataset().GetDatasetId(), DatasetIds: []string{datasetRsp.GetDataset().GetDatasetId()}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, viewRsp.GetRetInfo().GetCode())

	bindSubjectRsp, err := svc.BindDataSetSubject(context.Background(), &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), SubjectId: subjectRsp.GetSubject().GetSubjectId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, bindSubjectRsp.GetRetInfo().GetCode())

	fieldRsp, err := svc.CreateField(context.Background(), &pb.CreateFieldReq{Field: &pb.Field{SpaceId: spaceRsp.GetSpace().GetSpaceId(), FieldId: "close", Name: "收盘价", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, fieldRsp.GetRetInfo().GetCode())

	columnRsp, err := svc.UpsertDataSetColumn(context.Background(), &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: fieldRsp.GetField().GetFieldId(), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, columnRsp.GetRetInfo().GetCode())

	factorRsp, err := svc.CreateFactor(context.Background(), &pb.CreateFactorReq{Factor: &pb.Factor{SpaceId: spaceRsp.GetSpace().GetSpaceId(), FactorId: "ma20_close", Name: "MA20 收盘均线", Algorithm: "MA", ParamsJson: `{"window":20,"price":"close"}`, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, factorRsp.GetRetInfo().GetCode())

	nodeRsp, err := svc.CreateStorageNode(context.Background(), &pb.CreateStorageNodeReq{Node: &pb.StorageNode{Name: "primary-1", Endpoint: "127.0.0.1:19001"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, nodeRsp.GetRetInfo().GetCode())

	deviceRsp, err := svc.CreateDevice(context.Background(), &pb.CreateDeviceReq{Device: &pb.Device{Name: "pebble-1", NodeId: nodeRsp.GetNode().GetNodeId(), Engine: "pebble", Endpoint: "/tmp/pebble"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deviceRsp.GetRetInfo().GetCode())

	routeRsp, err := svc.CreateStorageRoute(context.Background(), &pb.CreateStorageRouteReq{StorageRoute: &pb.StorageRoute{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), NodeId: nodeRsp.GetNode().GetNodeId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, routeRsp.GetRetInfo().GetCode())

	archiveRsp, err := svc.RegisterArchiveFile(context.Background(), &pb.RegisterArchiveFileReq{ArchiveFile: &pb.ArchiveFile{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), DeviceId: deviceRsp.GetDevice().GetDeviceId(), PartitionKey: "date=2026-06-15", FileUri: "file:///archive/date=2026-06-15/part-000.parquet", Columns: []string{"close"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, archiveRsp.GetRetInfo().GetCode())

	listColumnsRsp, err := svc.ListDataSetColumns(context.Background(), &pb.ListDataSetColumnsReq{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId()})
	require.NoError(t, err)
	require.Len(t, listColumnsRsp.GetColumns(), 1)

	listArchiveRsp, err := svc.ListArchiveFiles(context.Background(), &pb.ListArchiveFilesReq{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId()})
	require.NoError(t, err)
	require.Len(t, listArchiveRsp.GetArchiveFiles(), 1)
}

func TestServicePersistsStorageNodeMetadataAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	svc := newTestServiceWithRoot(t, root)
	createRsp, err := svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{
		Node: &pb.StorageNode{Name: "primary-1", Endpoint: "127.0.0.1:19001"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	nodeID := createRsp.GetNode().GetNodeId()

	restarted := newTestServiceWithRoot(t, root)
	getRsp, err := restarted.GetStorageNode(ctx, &pb.GetStorageNodeReq{NodeId: nodeID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, getRsp.GetRetInfo().GetCode())
	require.Equal(t, nodeID, getRsp.GetNode().GetNodeId())
}

func TestServiceSearchRowsSupportsTextAndFilters(t *testing.T) {
	svc := newTestService(t)
	seedStringDataset(t, svc, "default", "binance_spot_symbols", []string{"symbol", "status", "base_asset"})
	_, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{
			{
				Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
				Columns: []*pb.ColumnValue{
					stringColumn("symbol", "APTUSDT"),
					stringColumn("status", "active"),
					stringColumn("base_asset", "APT"),
				},
			},
			{
				Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "AR-USDT"}, RowId: "AR-USDT"},
				Columns: []*pb.ColumnValue{
					stringColumn("symbol", "ARUSDT"),
					stringColumn("status", "inactive"),
					stringColumn("base_asset", "AR"),
				},
			},
		},
	})
	require.NoError(t, err)
	svc.WaitForIndex()

	searchRsp, err := svc.SearchRows(context.Background(), &pb.SearchRowsReq{
		SpaceId:     "default",
		DatasetId:   "binance_spot_symbols",
		TextQuery:   "USDT",
		ColumnNames: []string{"symbol", "status"},
		Filters: []*pb.FilterExpr{{
			Expr: "status == $status",
			Args: map[string]*pb.TypedValue{
				"status": {Value: &pb.TypedValue_StringValue{StringValue: "active"}},
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Len(t, searchRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", searchRsp.GetRows()[0].GetKey().GetScope().GetSubjectId())
	require.Len(t, searchRsp.GetRows()[0].GetColumns(), 2)
	require.Equal(t, "symbol", searchRsp.GetRows()[0].GetColumns()[0].GetColumnName())
	require.Equal(t, "status", searchRsp.GetRows()[0].GetColumns()[1].GetColumnName())
}

func seedStringDataset(t *testing.T, svc *Service, spaceID string, datasetID string, columns []string) {
	t.Helper()
	seedStringDatasetWithoutSubjects(t, svc, spaceID, datasetID, pb.DataKind_DATA_KIND_TABLE, columns)
	bindTestSubjects(t, svc, spaceID, datasetID, "APT-USDT", "AR-USDT")
	refreshMetadataCacheForTest(t, svc)
}

func seedStringDatasetWithoutSubjects(t *testing.T, svc *Service, spaceID string, datasetID string, dataKind pb.DataKind, columns []string) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: spaceID, Name: spaceID}})
	require.NoError(t, err)
	_, err = svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: spaceID, DataSourceId: "test_source", Name: "test_source", Kind: "manual"}})
	require.NoError(t, err)
	_, err = svc.CreateDataSet(ctx, &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: "test_source", Name: datasetID, DataKind: dataKind}})
	require.NoError(t, err)
	for _, column := range columns {
		_, err = svc.UpsertDataSetColumn(ctx, &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: spaceID, DatasetId: datasetID, ColumnName: column, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: column, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, TextIndexed: true}})
		require.NoError(t, err)
	}
	seedRoute(t, svc, spaceID, datasetID)
}

func bindTestSubjects(t *testing.T, svc *Service, spaceID string, datasetID string, subjectIDs ...string) {
	t.Helper()
	ctx := context.Background()
	for _, subjectID := range subjectIDs {
		_, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: spaceID, SubjectId: subjectID, Name: subjectID}})
		require.NoError(t, err)
		rsp, err := svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
}

func seedRoute(t *testing.T, svc *Service, spaceID string, datasetID string) {
	t.Helper()
	ctx := context.Background()
	nodeRsp, err := svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{Node: &pb.StorageNode{NodeId: "node_" + datasetID, Name: "node_" + datasetID, Endpoint: "local"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, nodeRsp.GetRetInfo().GetCode())
	deviceRsp, err := svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "device_" + datasetID, NodeId: nodeRsp.GetNode().GetNodeId(), Name: "pebble_" + datasetID, Engine: "pebble", Endpoint: "local"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deviceRsp.GetRetInfo().GetCode())
	routeRsp, err := svc.CreateStorageRoute(ctx, &pb.CreateStorageRouteReq{StorageRoute: &pb.StorageRoute{SpaceId: spaceID, DatasetId: datasetID, SubjectPattern: "*", NodeId: nodeRsp.GetNode().GetNodeId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, routeRsp.GetRetInfo().GetCode())
	refreshMetadataCacheForTest(t, svc)
}

func seedExactRoute(t *testing.T, svc *Service, spaceID string, datasetID string, subjectID string, nodeID string) {
	t.Helper()
	ctx := context.Background()
	nodeRsp, err := svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{Node: &pb.StorageNode{NodeId: nodeID, Name: nodeID, Endpoint: "local"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, nodeRsp.GetRetInfo().GetCode())
	deviceRsp, err := svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "device_" + nodeID, NodeId: nodeRsp.GetNode().GetNodeId(), Name: "pebble_" + nodeID, Engine: "pebble", Endpoint: "local"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deviceRsp.GetRetInfo().GetCode())
	routeRsp, err := svc.CreateStorageRoute(ctx, &pb.CreateStorageRouteReq{StorageRoute: &pb.StorageRoute{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, NodeId: nodeRsp.GetNode().GetNodeId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, routeRsp.GetRetInfo().GetCode())
	refreshMetadataCacheForTest(t, svc)
}

func refreshMetadataCacheForTest(t *testing.T, svc *Service) {
	t.Helper()
	if svc.metadataCache == nil {
		return
	}
	require.NoError(t, svc.metadataCache.Refresh(context.Background()))
}

func stringColumn(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}
