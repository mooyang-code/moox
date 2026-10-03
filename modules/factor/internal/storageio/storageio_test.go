package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

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
		{Rows: []*storagepb.TimeSeriesRow{
			readRow("ETH", base, "", map[string]*storagepb.TypedValue{"close": double(3)}),
			readRow("BTC", base.Add(time.Minute), "venue:b", map[string]*storagepb.TypedValue{"close": double(2)}),
		}, PageResult: &commonpb.PageResult{HasMore: true}},
		{Rows: []*storagepb.TimeSeriesRow{
			readRow("BTC", base, "venue:a", map[string]*storagepb.TypedValue{"close": double(1)}),
		}, PageResult: &commonpb.PageResult{HasMore: false}},
	}}
	got, err := NewClient(primary, nil, nil).ReadWindow(context.Background(), ReadRequest{
		SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"BTC", "ETH"},
		Start: base.Add(-time.Minute), End: base.Add(2 * time.Minute), Columns: []string{"close"},
	})
	require.NoError(t, err)
	require.Len(t, primary.readRequests, 2)
	require.NotEmpty(t, primary.readRequests[1].GetAfterKey())
	key := &storagepb.RowKey{}
	require.NoError(t, proto.Unmarshal(primary.readRequests[1].GetAfterKey(), key))
	require.Equal(t, "venue:b", key.GetTimeSeries().GetSeriesTag())
	require.Equal(t, []string{"data_time", "series_tag", "close"}, got["BTC"].Columns)
	require.Equal(t, []any{base, "venue:a", float64(1)}, got["BTC"].Rows[0])
	require.Equal(t, []any{base.Add(time.Minute), "venue:b", float64(2)}, got["BTC"].Rows[1])
	require.Equal(t, []any{base, "", float64(3)}, got["ETH"].Rows[0])
}

func TestReadWindowRequestsAllSelectorsWithoutSeriesTag(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	primary := &primaryFake{pages: []*storagepb.ReadTimeSeriesRowsRsp{{PageResult: &commonpb.PageResult{}}}}
	_, err := NewClient(primary, nil, nil).ReadWindow(context.Background(), ReadRequest{
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
		SpaceId: "crypto", DatasetId: "factor_result", Freqs: []string{"1m"}, Status: "active",
	}}
	store := NewClient(primary, metadata, nil)
	err := store.WriteRows(context.Background(), "crypto", "factor_result", "commit-1", []ResultRow{{
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

func TestCommitIDIsDeterministic(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	left := []ResultRow{{SubjectID: "BTC", DataTime: at, SeriesTag: "venue:a", Fields: map[string]any{"z": 2, "a": "x"}}}
	right := []ResultRow{{SubjectID: "BTC", DataTime: at, SeriesTag: "venue:a", Fields: map[string]any{"a": "x", "z": 2}}}
	require.Equal(t, CommitID("set-1", at.Unix(), left), CommitID("set-1", at.Unix(), right))
	require.Equal(t,
		CommitID("set-1", at.Unix(), []ResultRow{{SubjectID: "BTC", DataTime: at, Fields: map[string]any{"close": int(1)}}}),
		CommitID("set-1", at.Unix(), []ResultRow{{SubjectID: "BTC", DataTime: at, Fields: map[string]any{"close": int64(1)}}}),
	)
	require.Equal(t,
		CommitID("set-1", at.Unix(), []ResultRow{{SubjectID: "BTC", DataTime: at, Fields: map[string]any{"close": math.NaN()}}}),
		CommitID("set-1", at.Unix(), []ResultRow{{SubjectID: "BTC", DataTime: at, Fields: map[string]any{"close": nil}}}),
	)
	require.NotEqual(t, CommitID("set-1", at.Unix(), left), CommitID("set-2", at.Unix(), left))
}

func TestTransientErrorsWrapErrInfra(t *testing.T) {
	for _, err := range []error{
		errs.New(errs.RetClientNetErr, "unavailable"),
		context.DeadlineExceeded,
	} {
		t.Run(err.Error(), func(t *testing.T) {
			primary := &primaryFake{readErr: err}
			_, gotErr := NewClient(primary, nil, nil).ReadWindow(context.Background(), validReadRequest())
			require.ErrorIs(t, gotErr, ErrInfra)
		})
	}
	_, err := NewClient(&primaryFake{readRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "index not ready"}}, nil, nil).ReadWindow(context.Background(), validReadRequest())
	require.ErrorIs(t, err, ErrInfra)

	primary := &primaryFake{readRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "bad selector"}}
	_, err = NewClient(primary, nil, nil).ReadWindow(context.Background(), validReadRequest())
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInfra))
}

func TestReportComputedBuildsDeterministicEventID(t *testing.T) {
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m",
		PeriodTime: 1791115200, Status: "complete", TriggerEventID: "collector-event",
		ComputedAt: time.Date(2026, 10, 4, 12, 0, 1, 0, time.UTC),
	}
	primary := &primaryFake{}
	store := NewClient(primary, nil, nil)
	require.NoError(t, store.ReportComputed(context.Background(), marker))
	require.NoError(t, store.ReportComputed(context.Background(), marker))
	require.Equal(t, "storage-marker-"+shortHash("factor-period", "crypto", "factor_result", "collector-event", "1791115200"), primary.reportIDs[0])
	require.Equal(t, primary.reportIDs[0], primary.reportIDs[1])
}

func TestComputedExistsNotFoundIsFalse(t *testing.T) {
	for name, rsp := range map[string]*storagepb.GetFactorPeriodComputedRsp{
		"absent":         {RetInfo: successRet(), Found: false},
		"not found code": {RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "not found"}},
	} {
		t.Run(name, func(t *testing.T) {
			primary := &primaryFake{computedRsp: rsp}
			found, err := NewClient(primary, nil, nil).ComputedExists(context.Background(), "crypto", "factor_result", "collector-event", 123)
			require.NoError(t, err)
			require.False(t, found)
			require.Equal(t, "factor_result", primary.computedReq.GetDatasetId())
			require.Equal(t, int64(123), primary.computedReq.GetPeriodTime())
		})
	}
}

func TestDatasetColumnsReturnsOnlyActiveBusinessColumns(t *testing.T) {
	metadata := &metadataFake{columnPages: []*storagepb.ListDatasetColumnsRsp{
		{Columns: []*storagepb.DatasetColumn{
			{ColumnName: "close", OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "active"},
			{ColumnName: "data_time", OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, Status: "active"},
			{ColumnName: "disabled_col", OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "disabled"},
		}, PageResult: &commonpb.PageResult{HasMore: true}},
		{Columns: []*storagepb.DatasetColumn{
			{ColumnName: "open", OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "active"},
			{ColumnName: "subject_id", OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "active"},
		}, PageResult: &commonpb.PageResult{HasMore: false}},
	}}
	got, err := NewClient(nil, metadata, nil).DatasetColumns(context.Background(), "crypto", "bars")
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
	require.NoError(t, NewClient(nil, metadata, nil).CreateResultDataset(context.Background(), spec))
	require.Len(t, metadata.createRequests, 1)
	dataset := metadata.createRequests[0].GetDataset()
	require.Equal(t, spec.DataSourceID, dataset.GetDataSourceId())
	require.Equal(t, spec.DataNodeID, dataset.GetDataNodeId())
	require.Equal(t, spec.KeepDuration, dataset.GetKeepDuration())
	require.Equal(t, []string{spec.Frequency}, dataset.GetFreqs())
	require.Equal(t, []string{"tag-b", "tag-a"}, dataset.GetSubjectTags())
	require.Equal(t, DatasetRoleFactorResult, dataset.GetAttributes()["dataset_role"])
	require.Equal(t, spec.SourceDatasetID, dataset.GetAttributes()["source_dataset_id"])
	require.Len(t, metadata.upsertRequests, 1)
	require.Equal(t, "close", metadata.upsertRequests[0].GetColumn().GetColumnName())
}

func TestCreateResultDatasetRetryIsIdempotent(t *testing.T) {
	spec := resultDatasetSpec()
	existing := datasetFromResultSpec(spec)
	existing.Status = DatasetStatusActive
	metadata := &metadataFake{dataset: existing}
	require.NoError(t, NewClient(nil, metadata, nil).CreateResultDataset(context.Background(), spec))
	require.Empty(t, metadata.createRequests)
	require.Len(t, metadata.upsertRequests, 1)
}

func TestActivateDatasetUsesCurrentRevision(t *testing.T) {
	metadata := &metadataFake{dataset: &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: "factor_result", Revision: 12, Status: "disabled",
	}}
	require.NoError(t, NewClient(nil, metadata, nil).ActivateDataset(context.Background(), "crypto", "factor_result"))
	require.Equal(t, uint64(12), metadata.activateRequests[0].GetExpectedRevision())
}

func validReadRequest() ReadRequest {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	return ReadRequest{SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Subjects: []string{"BTC"}, Start: now, End: now.Add(time.Minute), Columns: []string{"close"}}
}

func resultDatasetSpec() ResultDatasetSpec {
	return ResultDatasetSpec{
		SpaceID: "crypto", DatasetID: "factor_result", SourceDatasetID: "bars", Name: "因子结果",
		Description: "derived factors", DataSourceID: "binance", DataNodeID: "storage-node-0",
		DataKind: DataKindTimeSeries, Frequency: "1m", KeepDuration: "720h",
		SubjectTags: []string{"tag-b", "tag-a"}, Attributes: map[string]string{"owner": "factor"},
		Columns: []ColumnInfo{{ColumnName: "close", OriginType: ColumnOriginField, OriginID: "close", ValueType: ColumnTypeDouble}},
	}
}

type primaryFake struct {
	pages         []*storagepb.ReadTimeSeriesRowsRsp
	readRequests  []*storagepb.ReadTimeSeriesRowsReq
	readErr       error
	readRet       *commonpb.RetInfo
	writeRequests []*storagepb.PrimaryWriteFactorRowsReq
	writeErr      error
	writeRet      *commonpb.RetInfo
	reportIDs     []string
	computedRsp   *storagepb.GetFactorPeriodComputedRsp
	computedReq   *storagepb.GetFactorPeriodComputedReq
}

type metadataFake struct {
	dataset          *storagepb.Dataset
	getDatasetRsps   []*storagepb.GetDatasetRsp
	columnPages      []*storagepb.ListDatasetColumnsRsp
	columnRequests   []*storagepb.ListDatasetColumnsReq
	createRequests   []*storagepb.CreateDatasetReq
	createRsp        *storagepb.CreateDatasetRsp
	upsertRequests   []*storagepb.UpsertDatasetColumnReq
	activateRequests []*storagepb.ActivateDatasetReq
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

func (f *metadataFake) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq, _ ...client.Option) (*storagepb.ActivateDatasetRsp, error) {
	f.activateRequests = append(f.activateRequests, proto.Clone(req).(*storagepb.ActivateDatasetReq))
	return &storagepb.ActivateDatasetRsp{RetInfo: successRet()}, nil
}

func (*metadataFake) DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error) {
	return &storagepb.DeleteDatasetRsp{RetInfo: successRet()}, nil
}

func (f *primaryFake) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readRequests = append(f.readRequests, proto.Clone(req).(*storagepb.ReadTimeSeriesRowsReq))
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

func (f *primaryFake) WriteFactorRows(_ context.Context, req *storagepb.PrimaryWriteFactorRowsReq, _ ...client.Option) (*storagepb.PrimaryWriteFactorRowsRsp, error) {
	f.writeRequests = append(f.writeRequests, proto.Clone(req).(*storagepb.PrimaryWriteFactorRowsReq))
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	ret := f.writeRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.PrimaryWriteFactorRowsRsp{RetInfo: ret, RowsWritten: uint64(len(req.GetRows()))}, nil
}

func (f *primaryFake) ReportFactorPeriodComputed(_ context.Context, req *storagepb.ReportFactorPeriodComputedReq, _ ...client.Option) (*storagepb.ReportFactorPeriodComputedRsp, error) {
	id := computedEventID(req.GetSpaceId(), req.GetMarker())
	f.reportIDs = append(f.reportIDs, id)
	return &storagepb.ReportFactorPeriodComputedRsp{RetInfo: successRet(), EventId: id}, nil
}

func (f *primaryFake) GetFactorPeriodComputed(_ context.Context, req *storagepb.GetFactorPeriodComputedReq, _ ...client.Option) (*storagepb.GetFactorPeriodComputedRsp, error) {
	f.computedReq = proto.Clone(req).(*storagepb.GetFactorPeriodComputedReq)
	if f.computedRsp != nil {
		return f.computedRsp, nil
	}
	return &storagepb.GetFactorPeriodComputedRsp{RetInfo: successRet()}, nil
}

func readRow(subject string, at time.Time, tag string, fields map[string]*storagepb.TypedValue) *storagepb.TimeSeriesRow {
	values := make([]*storagepb.FieldValue, 0, len(fields))
	for name, value := range fields {
		values = append(values, &storagepb.FieldValue{FieldId: name, Value: value})
	}
	return &storagepb.TimeSeriesRow{
		Key:    &storagepb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "bars", SubjectId: subject, Freq: "1m", DataTime: at.Format(time.RFC3339Nano), SeriesTag: tag},
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
