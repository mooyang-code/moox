package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/errs"
)

func TestReadWindowPagesUntilExhausted(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{
		{Rows: []*storagepb.Row{
			readRow("ETH", base, "", map[string]*storagepb.TypedValue{"close": double(3)}),
			readRow("BTC", base.Add(time.Minute), "venue:b", map[string]*storagepb.TypedValue{"close": double(2)}),
		}, PageResult: &commonpb.PageResult{HasMore: true, NextCursor: "cursor-1"}},
		{Rows: []*storagepb.Row{
			readRow("BTC", base, "venue:a", map[string]*storagepb.TypedValue{"close": double(1)}),
		}, PageResult: &commonpb.PageResult{HasMore: false}},
	}}
	got, err := NewClient(primary, nil).ReadWindow(context.Background(), ReadRequest{
		SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"BTC", "ETH"},
		Start: base.Add(-time.Minute), End: base.Add(2 * time.Minute), Columns: []string{"close"},
	})
	require.NoError(t, err)
	require.Len(t, primary.readRequests, 2)
	require.Empty(t, primary.readRequests[0].GetPage().GetCursor())
	require.Equal(t, "cursor-1", primary.readRequests[1].GetPage().GetCursor(), "游标原样传回，不自行编码")
	require.Equal(t, []string{"data_time", "series_tag", "close"}, got["BTC"].Columns)
	require.Equal(t, []any{base, "venue:a", float64(1)}, got["BTC"].Rows[0])
	require.Equal(t, []any{base.Add(time.Minute), "venue:b", float64(2)}, got["BTC"].Rows[1])
	require.Equal(t, []any{base, "", float64(3)}, got["ETH"].Rows[0])
}

func TestReadWindowBoundsEachPageWithPageTimeout(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{
		{Rows: []*storagepb.Row{readRow("BTC", base, "", map[string]*storagepb.TypedValue{"close": double(1)})}, PageResult: &commonpb.PageResult{HasMore: true, NextCursor: "cursor-1"}},
		{Rows: []*storagepb.Row{readRow("BTC", base.Add(time.Minute), "", map[string]*storagepb.TypedValue{"close": double(2)})}, PageResult: &commonpb.PageResult{HasMore: false}},
	}}
	_, err := NewClient(primary, nil).ReadWindow(context.Background(), ReadRequest{
		SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"BTC"},
		Start: base, End: base.Add(2 * time.Minute), Columns: []string{"close"}, PageTimeout: 7 * time.Second,
	})

	require.NoError(t, err)
	require.Len(t, primary.readDeadlines, 2, "every page carries its own deadline")
	for _, remaining := range primary.readDeadlines {
		require.InDelta(t, float64(7*time.Second), float64(remaining), float64(time.Second))
	}
}

func TestEarliestPeriodUsesBoundedAscendingSubjectBatches(t *testing.T) {
	earlier := time.Date(2025, 2, 3, 4, 5, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{
		{Rows: []*storagepb.Row{readRow("subject-000", later, "", map[string]*storagepb.TypedValue{"close": double(1)})}},
		{Rows: []*storagepb.Row{readRow("subject-128", earlier, "", map[string]*storagepb.TypedValue{"close": double(1)})}},
	}}
	subjects := make([]string, 129)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("subject-%03d", i)
	}

	got, found, err := NewClient(primary, nil).EarliestPeriod(context.Background(), "crypto", "bars", "1m", subjects, []string{"close"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, earlier, got)
	require.Len(t, primary.readRequests, 2)
	for _, request := range primary.readRequests {
		require.Equal(t, uint32(1), request.GetPage().GetSize())
		require.Equal(t, "0001-01-01T00:00:00Z", request.GetTimeRange().GetStartTime())
		require.Equal(t, "1m", request.GetSelectors()[0].GetFreq())
	}
	require.Len(t, primary.readRequests[0].GetSelectors(), earliestPeriodSelectorBatchSize)
	require.Len(t, primary.readRequests[1].GetSelectors(), 29)
}

func TestReadWindowSplitsSubjectsIntoSelectorBatches(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{
		{PageResult: &commonpb.PageResult{}}, {PageResult: &commonpb.PageResult{}}, {PageResult: &commonpb.PageResult{}},
	}}
	subjects := make([]string, 250)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("subject-%03d", i)
	}
	frames, err := NewClient(primary, nil).ReadWindow(context.Background(), ReadRequest{
		SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: subjects,
		Start: base, End: base.Add(time.Minute), Columns: []string{"close"},
	})
	require.NoError(t, err)
	require.Len(t, frames, 250)
	require.Len(t, primary.readRequests, 3)
	for i, want := range []int{100, 100, 50} {
		require.Len(t, primary.readRequests[i].GetSelectors(), want)
		require.LessOrEqual(t, want, readSelectorBatchSize)
	}
}

func TestReadWindowRequestsAllSelectorsWithoutSeriesTag(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{{PageResult: &commonpb.PageResult{}}}}
	_, err := NewClient(primary, nil).ReadWindow(context.Background(), ReadRequest{
		SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"ETH", "BTC", "BTC"},
		Start: base, End: base.Add(time.Minute), Columns: []string{"close"},
	})
	require.NoError(t, err)
	selectors := primary.readRequests[0].GetSelectors()
	require.Len(t, selectors, 2)
	require.Equal(t, []string{"BTC", "ETH"}, []string{selectors[0].GetSubjectId(), selectors[1].GetSubjectId()})
	for _, selector := range selectors {
		require.Nil(t, selector.SeriesTag)
		require.Equal(t, "1m", selector.GetFreq())
	}
	require.Equal(t, base.Format(time.RFC3339Nano), primary.readRequests[0].GetTimeRange().GetStartTime())
	require.Equal(t, base.Add(time.Minute).Format(time.RFC3339Nano), primary.readRequests[0].GetTimeRange().GetEndTime())
}

func TestWriteRowsEncodesNullFields(t *testing.T) {
	primary := &primaryFake{}
	metadata := &metadataFake{dataset: &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Freq: "1m", Status: "active",
	}}
	store := NewClient(primary, metadata)
	err := store.WriteRows(context.Background(), "crypto", "factor_result", []ResultRow{{
		SubjectID: "BTC", DataTime: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), SeriesTag: "venue:a",
		Fields: map[string]any{"close": nil, "high": math.Inf(1), "low": math.NaN()},
	}})
	require.NoError(t, err)
	require.Len(t, primary.writeRequests, 1)
	row := primary.writeRequests[0].GetRows()[0]
	require.Equal(t, "1m", row.GetKey().GetTimeSeries().GetFreq())
	fields := row.GetFields()
	require.Len(t, fields, 3)
	for _, field := range fields {
		require.IsType(t, &storagepb.TypedValue_NullValue{}, field.GetValue().GetValue())
		require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, field.GetValue().GetNullValue())
	}
}

func TestTransientErrorsWrapErrInfra(t *testing.T) {
	for _, err := range []error{
		errs.New(errs.RetClientNetErr, "unavailable"),
		context.DeadlineExceeded,
	} {
		t.Run(err.Error(), func(t *testing.T) {
			primary := &primaryFake{readErr: err}
			_, gotErr := NewClient(primary, nil).ReadWindow(context.Background(), validReadRequest())
			require.ErrorIs(t, gotErr, ErrInfra)
		})
	}
	_, err := NewClient(&primaryFake{readRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "index not ready"}}, nil).ReadWindow(context.Background(), validReadRequest())
	require.ErrorIs(t, err, ErrInfra)

	primary := &primaryFake{readRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "bad selector"}}
	_, err = NewClient(primary, nil).ReadWindow(context.Background(), validReadRequest())
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInfra))
}

func TestReportComputedBuildsDeterministicEventID(t *testing.T) {
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m",
		PeriodTime: 1791115200, Status: "complete",
		ComputedAt: time.Date(2026, 10, 4, 12, 0, 1, 0, time.UTC),
	}
	primary := &primaryFake{}
	store := NewClient(primary, nil)
	require.NoError(t, store.ReportComputed(context.Background(), marker))
	require.NoError(t, store.ReportComputed(context.Background(), marker))
	require.Equal(t, "storage-marker-"+shortHash("producer-period", "crypto", "factor_result", PeriodCauseID("factor_result", 1791115200), "1791115200"), primary.reportIDs[0])
	require.Equal(t, primary.reportIDs[0], primary.reportIDs[1])
}

// Storage 判为无效请求（例如完成标记通不过事件校验）不是基础设施错误：重试也不会通过，调用方不重试；内部错误照常重试。
func TestReportComputedInvalidRequestIsNotRetried(t *testing.T) {
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m",
		PeriodTime: 1791115200, Status: "complete",
	}
	primary := &primaryFake{reportRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "完成标记无法通过事件校验"}}
	err := NewClient(primary, nil).ReportComputed(context.Background(), marker)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInfra))

	primary = &primaryFake{reportRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "pebble unavailable"}}
	err = NewClient(primary, nil).ReportComputed(context.Background(), marker)
	require.ErrorIs(t, err, ErrInfra)
}

func TestComputedExistsNotFoundIsFalse(t *testing.T) {
	for name, rsp := range map[string]*storagepb.GetDatasetPeriodCompletedRsp{
		"absent":         {RetInfo: successRet(), Found: false},
		"not found code": {RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "not found"}},
	} {
		t.Run(name, func(t *testing.T) {
			primary := &primaryFake{computedRsp: rsp}
			found, err := NewClient(primary, nil).ComputedExists(context.Background(), "crypto", "factor_result", 123)
			require.NoError(t, err)
			require.False(t, found)
			require.Equal(t, "factor_result", primary.computedReq.GetDatasetId())
			require.Equal(t, PeriodCauseID("factor_result", 123), primary.computedReq.GetCauseEventId())
			require.Equal(t, int64(123), primary.computedReq.GetPeriodTime())
		})
	}
}

func TestDatasetColumnsReturnsOnlyActiveBusinessColumns(t *testing.T) {
	metadata := &metadataFake{columnPages: []*storagepb.ListDatasetColumnsRsp{
		{Columns: []*storagepb.DatasetColumn{
			{ColumnName: "close", Status: "active"},
			{ColumnName: "data_time", Status: "active"},
			{ColumnName: "disabled_col", Status: "disabled"},
		}, PageResult: &commonpb.PageResult{HasMore: true}},
		{Columns: []*storagepb.DatasetColumn{
			{ColumnName: "open", Status: "active"},
			{ColumnName: "subject_id", Status: "active"},
		}, PageResult: &commonpb.PageResult{HasMore: false}},
	}}
	got, err := NewClient(nil, metadata).DatasetColumns(context.Background(), "crypto", "bars")
	require.NoError(t, err)
	require.Equal(t, []string{"close", "open"}, got)
	require.Len(t, metadata.columnRequests, 2)
	require.Equal(t, uint32(1), metadata.columnRequests[0].GetPage().GetPage())
	require.Equal(t, uint32(2), metadata.columnRequests[1].GetPage().GetPage())
}

func TestCreateResultDatasetUsesSourceOwnershipAndRetention(t *testing.T) {
	spec := resultDatasetSpec()
	metadata := &metadataFake{
		getDatasetRsps: []*storagepb.GetDatasetRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "missing"}}},
	}
	require.NoError(t, NewClient(nil, metadata).CreateResultDataset(context.Background(), spec))
	require.Len(t, metadata.createRequests, 1)
	dataset := metadata.createRequests[0].GetDataset()
	require.Equal(t, spec.DataSourceID, dataset.GetDataSourceId())
	require.Equal(t, DatasetStatusActive, dataset.GetStatus())
	require.Equal(t, spec.Frequency, dataset.GetFreq())
	require.Equal(t, []string{"tag-b", "tag-a"}, dataset.GetSubjectTags())
	require.Equal(t, DatasetRoleFactorResult, dataset.GetAttributes()["dataset_role"])
	require.Equal(t, spec.SourceDatasetID, dataset.GetAttributes()["source_dataset_id"])
	require.Len(t, metadata.createRequests[0].GetColumns(), 1, "列随数据集一次建好")
	require.Equal(t, "close", metadata.createRequests[0].GetColumns()[0].GetColumnName())
	require.Equal(t, "收盘价", metadata.createRequests[0].GetColumns()[0].GetDisplayName())
	require.Equal(t, "行情价格", metadata.createRequests[0].GetColumns()[0].GetGroup())
	require.Equal(t, uint32(10), dataset.GetMaintenancePriority(), "因子结果的查询索引排在采集结果之后维护")
	require.Empty(t, metadata.upsertRequests)
}

func TestCreateResultDatasetTreatsDatasetNotFoundAsCreateable(t *testing.T) {
	spec := resultDatasetSpec()
	metadata := &metadataFake{
		getDatasetRsps: []*storagepb.GetDatasetRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: "missing"}}},
	}
	require.NoError(t, NewClient(nil, metadata).CreateResultDataset(context.Background(), spec))
	require.Len(t, metadata.createRequests, 1)
}

// 行由 Storage 随数据集一起删，客户端只调一次 DeleteDataset。
func TestDeleteFactorResultDatasetIsOneStorageCall(t *testing.T) {
	dataset := &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Attributes: map[string]string{"dataset_role": DatasetRoleFactorResult},
	}
	metadata := &metadataFake{getDatasetRsps: []*storagepb.GetDatasetRsp{{RetInfo: successRet(), Dataset: dataset}}}
	client := NewClient(&primaryFake{}, metadata)

	require.NoError(t, client.DeleteDataset(context.Background(), "crypto", "factor_result"))
	require.Len(t, metadata.deleteRequests, 1)
}

// 删除中途失败时重试同一个调用即可完成。
func TestDeleteFactorResultDatasetRetriesAfterFailure(t *testing.T) {
	dataset := &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Attributes: map[string]string{"dataset_role": DatasetRoleFactorResult},
	}
	metadata := &metadataFake{
		dataset: dataset,
		deleteRsps: []*storagepb.DeleteDatasetRsp{
			{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "metadata unavailable"}},
			{RetInfo: successRet()},
		},
	}
	client := NewClient(&primaryFake{}, metadata)

	err := client.DeleteDataset(context.Background(), "crypto", "factor_result")
	require.ErrorContains(t, err, "metadata unavailable")
	require.NoError(t, client.DeleteDataset(context.Background(), "crypto", "factor_result"))
	require.Len(t, metadata.deleteRequests, 2)
}

// 元数据已经不在时仍要调 DeleteDataset：上一次可能删完元数据后删行失败，Storage 对不存在的数据集也会删行。
func TestDeleteDatasetStillCallsStorageWhenMetadataIsGone(t *testing.T) {
	metadata := &metadataFake{
		getDatasetRsps: []*storagepb.GetDatasetRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}}},
		deleteRsp:      &storagepb.DeleteDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND}},
	}
	client := NewClient(&primaryFake{}, metadata)
	require.NoError(t, client.DeleteDataset(context.Background(), "crypto", "factor_result"))
	require.Len(t, metadata.deleteRequests, 1)
}

// 第一次删完元数据后失败（缓存发布或删行出错），重试时 GetDataset 已是不存在，仍要再发一次 DeleteDataset。
func TestDeleteDatasetRetryAfterMetadataDeletedStillDeletesRows(t *testing.T) {
	dataset := &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Attributes: map[string]string{"dataset_role": DatasetRoleFactorResult},
	}
	metadata := &metadataFake{
		getDatasetRsps: []*storagepb.GetDatasetRsp{
			{RetInfo: successRet(), Dataset: dataset},
			{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}},
		},
		deleteRsps: []*storagepb.DeleteDatasetRsp{
			{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "数据集元数据已删除，删行失败，请重试删除"}},
			{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND}},
		},
	}
	client := NewClient(&primaryFake{}, metadata)
	require.Error(t, client.DeleteDataset(context.Background(), "crypto", "factor_result"))
	require.NoError(t, client.DeleteDataset(context.Background(), "crypto", "factor_result"))
	require.Len(t, metadata.deleteRequests, 2, "重试必须再调一次 DeleteDataset 删掉行")
}

func TestCreateResultDatasetRetryIsIdempotent(t *testing.T) {
	spec := resultDatasetSpec()
	existing := datasetFromResultSpec(spec)
	existing.Status = DatasetStatusActive
	metadata := &metadataFake{dataset: existing}
	require.NoError(t, NewClient(nil, metadata).CreateResultDataset(context.Background(), spec))
	require.Empty(t, metadata.createRequests)
	require.Len(t, metadata.upsertRequests, 1)
}

func TestSetDatasetSubjectTagsKeepsOtherFieldsAndRevision(t *testing.T) {
	metadata := &metadataFake{dataset: &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Name: "因子结果", Revision: 7, Status: "active",
		Attributes: map[string]string{"dataset_role": DatasetRoleFactorResult},
	}}
	require.NoError(t, NewClient(nil, metadata).SetDatasetSubjectTags(context.Background(), "crypto", "factor_result", []string{"binance_spot"}))
	require.Len(t, metadata.updateRequests, 1)
	updated := metadata.updateRequests[0].GetDataset()
	require.Equal(t, []string{"binance_spot"}, updated.GetSubjectTags())
	require.Equal(t, uint64(7), updated.GetRevision(), "the update carries the revision it read")
	require.Equal(t, "因子结果", updated.GetName())
	require.Empty(t, metadata.dataset.GetSubjectTags(), "the read dataset is not mutated in place")
}

func validReadRequest() ReadRequest {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	return ReadRequest{SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"BTC"}, Start: now, End: now.Add(time.Minute), Columns: []string{"close"}}
}

func resultDatasetSpec() ResultDatasetSpec {
	return ResultDatasetSpec{
		SpaceID: "crypto", DatasetID: "factor_result", SourceDatasetID: "bars", Name: "因子结果",
		Description: "derived factors", DataSourceID: "binance",
		DataKind: DataKindTimeSeries, Frequency: "1m",
		SubjectTags: []string{"tag-b", "tag-a"}, Attributes: map[string]string{"owner": "factor"},
		Columns: []ColumnInfo{{ColumnName: "close", DisplayName: "收盘价", Group: "行情价格", ValueType: ColumnTypeDouble}},
	}
}

type primaryFake struct {
	changeRequests []*storagepb.ReadDatasetChangesReq
	changesRsp     *storagepb.ReadDatasetChangesRsp
	pages          []*storagepb.ReadTimeSeriesRowsRsp
	readRequests   []*storagepb.ReadTimeSeriesRowsReq
	readErr        error
	readRet        *commonpb.RetInfo
	writeRequests  []*storagepb.PrimaryUpsertFieldsReq
	writeErr       error
	writeRet       *commonpb.RetInfo
	reportIDs      []string
	reportRet      *commonpb.RetInfo
	computedRsp    *storagepb.GetDatasetPeriodCompletedRsp
	computedReq    *storagepb.GetDatasetPeriodCompletedReq
	readDeadlines  []time.Duration
}

type metadataFake struct {
	dataset        *storagepb.Dataset
	getDatasetRsps []*storagepb.GetDatasetRsp
	columnPages    []*storagepb.ListDatasetColumnsRsp
	columnRequests []*storagepb.ListDatasetColumnsReq
	createRequests []*storagepb.CreateDatasetReq
	createRsp      *storagepb.CreateDatasetRsp
	upsertRequests []*storagepb.UpsertDatasetColumnReq
	deleteRequests []*storagepb.DeleteDatasetReq
	deleteRsp      *storagepb.DeleteDatasetRsp
	deleteRsps     []*storagepb.DeleteDatasetRsp
	subjectPages   []*storagepb.ListDatasetSubjectsRsp
	updateRequests []*storagepb.UpdateDatasetReq
}

func (f *metadataFake) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq, _ ...client.Option) (*storagepb.UpdateDatasetRsp, error) {
	f.updateRequests = append(f.updateRequests, req)
	return &storagepb.UpdateDatasetRsp{RetInfo: successRet(), Dataset: req.GetDataset()}, nil
}

func (f *metadataFake) ListDatasetSubjects(_ context.Context, req *storagepb.ListDatasetSubjectsReq, _ ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	page := int(req.GetPage().GetPage()) - 1
	if page < 0 || page >= len(f.subjectPages) {
		return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	}
	return f.subjectPages[page], nil
}

func (f *metadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	if len(f.getDatasetRsps) > 0 {
		rsp := f.getDatasetRsps[0]
		f.getDatasetRsps = f.getDatasetRsps[1:]
		return rsp, nil
	}
	if f.dataset != nil {
		return &storagepb.GetDatasetRsp{RetInfo: successRet(), Dataset: f.dataset}, nil
	}
	if req.GetDatasetId() == "factor_result" {
		return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "missing"}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: successRet(), Dataset: f.dataset}, nil
}

func (f *metadataFake) ListDatasetColumns(_ context.Context, req *storagepb.ListDatasetColumnsReq, _ ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	f.columnRequests = append(f.columnRequests, proto.Clone(req).(*storagepb.ListDatasetColumnsReq))
	if len(f.columnPages) > 0 {
		rsp := f.columnPages[0]
		f.columnPages = f.columnPages[1:]
		if rsp.GetRetInfo() == nil {
			rsp.RetInfo = successRet()
		}
		return rsp, nil
	}
	return &storagepb.ListDatasetColumnsRsp{RetInfo: successRet()}, nil
}

func (f *metadataFake) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq, _ ...client.Option) (*storagepb.CreateDatasetRsp, error) {
	f.createRequests = append(f.createRequests, proto.Clone(req).(*storagepb.CreateDatasetReq))
	if f.createRsp != nil {
		return f.createRsp, nil
	}
	return &storagepb.CreateDatasetRsp{RetInfo: successRet(), Dataset: req.GetDataset()}, nil
}

func (f *metadataFake) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq, _ ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.upsertRequests = append(f.upsertRequests, proto.Clone(req).(*storagepb.UpsertDatasetColumnReq))
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: successRet(), Column: req.GetColumn()}, nil
}

func (f *metadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq, _ ...client.Option) (*storagepb.DeleteDatasetRsp, error) {
	f.deleteRequests = append(f.deleteRequests, proto.Clone(req).(*storagepb.DeleteDatasetReq))
	if len(f.deleteRsps) > 0 {
		response := f.deleteRsps[0]
		f.deleteRsps = f.deleteRsps[1:]
		return response, nil
	}
	if f.deleteRsp != nil {
		return f.deleteRsp, nil
	}
	return &storagepb.DeleteDatasetRsp{RetInfo: successRet()}, nil
}

func (f *primaryFake) ReadDatasetChanges(_ context.Context, req *storagepb.ReadDatasetChangesReq, _ ...client.Option) (*storagepb.ReadDatasetChangesRsp, error) {
	f.changeRequests = append(f.changeRequests, proto.Clone(req).(*storagepb.ReadDatasetChangesReq))
	if f.changesRsp != nil {
		return f.changesRsp, nil
	}
	return &storagepb.ReadDatasetChangesRsp{RetInfo: successRet()}, nil
}

func (f *primaryFake) ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readRequests = append(f.readRequests, proto.Clone(req).(*storagepb.ReadTimeSeriesRowsReq))
	if deadline, ok := ctx.Deadline(); ok {
		f.readDeadlines = append(f.readDeadlines, time.Until(deadline))
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	if len(f.pages) == 0 {
		ret := f.readRet
		if ret == nil {
			ret = successRet()
		}
		return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: ret, PageResult: &commonpb.PageResult{}}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	if page.GetRetInfo() == nil {
		page.RetInfo = f.readRet
		if page.RetInfo == nil {
			page.RetInfo = successRet()
		}
	}
	return page, nil
}

func (f *primaryFake) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.writeRequests = append(f.writeRequests, proto.Clone(req).(*storagepb.PrimaryUpsertFieldsReq))
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	ret := f.writeRet
	if ret == nil {
		ret = successRet()
	}
	keys := make([]*storagepb.RowKey, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		keys = append(keys, row.GetKey())
	}
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: ret, Keys: keys}, nil
}

func (f *primaryFake) ReportDatasetPeriodCompleted(_ context.Context, req *storagepb.ReportDatasetPeriodCompletedReq, _ ...client.Option) (*storagepb.ReportDatasetPeriodCompletedRsp, error) {
	id := computedEventID(req.GetSpaceId(), req.GetMarker())
	f.reportIDs = append(f.reportIDs, id)
	if f.reportRet != nil {
		return &storagepb.ReportDatasetPeriodCompletedRsp{RetInfo: f.reportRet}, nil
	}
	return &storagepb.ReportDatasetPeriodCompletedRsp{RetInfo: successRet(), EventId: id}, nil
}

func (f *primaryFake) GetDatasetPeriodCompleted(_ context.Context, req *storagepb.GetDatasetPeriodCompletedReq, _ ...client.Option) (*storagepb.GetDatasetPeriodCompletedRsp, error) {
	f.computedReq = proto.Clone(req).(*storagepb.GetDatasetPeriodCompletedReq)
	if f.computedRsp != nil {
		return f.computedRsp, nil
	}
	return &storagepb.GetDatasetPeriodCompletedRsp{RetInfo: successRet()}, nil
}

func readRow(subject string, at time.Time, tag string, fields map[string]*storagepb.TypedValue) *storagepb.Row {
	values := make([]*storagepb.FieldValue, 0, len(fields))
	for name, value := range fields {
		values = append(values, &storagepb.FieldValue{FieldId: name, Value: value})
	}
	return &storagepb.Row{
		Key: &storagepb.RowKey{
			SpaceId: "crypto", DatasetId: "bars",
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: at.Format(time.RFC3339Nano), SeriesTag: tag}},
		},
		Fields: values,
	}
}

func double(value float64) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}
}

func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func shortHash(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:16])
}

func TestListDatasetSubjectsPagesActiveSubjects(t *testing.T) {
	ok := &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	meta := &metadataFake{subjectPages: []*storagepb.ListDatasetSubjectsRsp{
		{RetInfo: ok, DatasetSubjects: []*storagepb.DatasetSubject{{SubjectId: "ETH", Status: "active"}, {SubjectId: "OLD", Status: "inactive"}},
			PageResult: &commonpb.PageResult{HasMore: true}},
		{RetInfo: ok, DatasetSubjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}}},
	}}
	c := NewClient(nil, meta)

	subjects, err := c.ListDatasetSubjects(context.Background(), "crypto", "dataset_bars")

	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH"}, subjects)
}

// 周期明细里成员的失败标的去重排序后写出（周期级没有失败标的时成员名单原样保留）。
func TestMarkerToProtoSortsFailedSubjects(t *testing.T) {
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 1791115200, Status: "degraded", UniverseSubjects: []string{"BTC", "ETH"},
		Factors: []FactorState{{FactorID: "momentum", MemberKey: "momentum", MemberKind: "fixed", Status: "degraded", FailedSubjects: []string{"ETH", "BTC", "ETH"}, SourceHash: "s", DefinitionHash: "d"}},
	}
	protoMarker, err := markerToProto(marker)
	require.NoError(t, err)
	require.Equal(t, PeriodDetailType, protoMarker.GetDetailType())
	detail := &factorpb.PeriodDetail{}
	require.NoError(t, proto.Unmarshal(protoMarker.GetDetail(), detail))
	got := detail.GetFactors()
	require.Len(t, got, 1)
	require.Equal(t, []string{"BTC", "ETH"}, got[0].GetFailedSubjects())
}

func TestCarriedTimeAndJSONFieldsKeepTheirStorageType(t *testing.T) {
	timeValue, err := encodeValue(typedValueToAny(&storagepb.TypedValue{Value: &storagepb.TypedValue_TimeValue{TimeValue: "2026-10-04T00:00:00Z"}}))
	require.NoError(t, err)
	require.Equal(t, "2026-10-04T00:00:00Z", timeValue.GetTimeValue())

	jsonValue, err := encodeValue(typedValueToAny(&storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: `{"a":1}`}}))
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, jsonValue.GetJsonValue())
}

func TestReadDatasetChangesMapsRequestAndResponse(t *testing.T) {
	primary := &primaryFake{changesRsp: &storagepb.ReadDatasetChangesRsp{
		RetInfo: successRet(), HasMore: true,
		NextCursor: &storagepb.DatasetChangeCursor{StoreId: "s1", Sequence: 9},
		// 完成标记与记录型行条目被忽略，只留时间序列行条目。
		Changes: []*storagepb.DatasetChange{
			{Sequence: 6, DatasetId: "d", Marker: []byte("marker")},
			{Sequence: 7, DatasetId: "d", Records: []*storagepb.RecordRowKey{{RecordId: "r", Version: "1"}}},
			{Sequence: 8, DatasetId: "d", Ranges: []*storagepb.DatasetChangeRange{
				{SubjectId: "BTC", Freq: "1m", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-01T00:05:00.5Z"},
			}},
		},
	}}
	c := NewClient(primary, &metadataFake{})
	got, err := c.ReadDatasetChanges(context.Background(), "crypto", "d", &ChangeCursor{StoreID: "s1", Sequence: 7}, false, 100)
	require.NoError(t, err)
	require.Equal(t, uint32(100), primary.changeRequests[0].GetLimit())
	require.Equal(t, uint64(7), primary.changeRequests[0].GetCursor().GetSequence())
	require.False(t, primary.changeRequests[0].GetFromHead())
	require.True(t, got.HasMore)
	require.Equal(t, ChangeCursor{StoreID: "s1", Sequence: 9}, got.Next)
	require.Len(t, got.Changes, 1)
	require.Equal(t, uint64(8), got.Changes[0].Sequence)
	require.Equal(t, "BTC", got.Changes[0].Ranges[0].Subject)
	require.Equal(t, time.Date(2026, 10, 1, 0, 5, 0, 500_000_000, time.UTC), got.Changes[0].Ranges[0].End)

	_, err = c.ReadDatasetChanges(context.Background(), "crypto", "d", nil, true, 0)
	require.NoError(t, err)
	require.True(t, primary.changeRequests[1].GetFromHead())

	primary.changesRsp = &storagepb.ReadDatasetChangesRsp{RetInfo: successRet(), Gap: true}
	got, err = c.ReadDatasetChanges(context.Background(), "crypto", "d", &ChangeCursor{Sequence: 1}, false, 0)
	require.NoError(t, err)
	require.True(t, got.Gap)

	_, err = c.ReadDatasetChanges(context.Background(), "", "d", nil, true, 0)
	require.Error(t, err)
}
