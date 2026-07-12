package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeSeriesKeyAdapterRoundTrip(t *testing.T) {
	row := &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m",
			DataTime: "2026-07-10T12:00:00Z", Dimensions: map[string]string{"venue": "binance"},
		},
		Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		Attributes: map[string]string{"source": "test"},
	}

	primaryRow, err := timeSeriesRowToPrimaryStoreRow(row)
	require.NoError(t, err)
	assert.Equal(t, "crypto", primaryRow.GetKey().GetSpaceId())
	assert.Contains(t, primaryRow.GetAttributes(), timeSeriesDimensionsAttribute)

	restored := primaryStoreRowToTimeSeriesRow(primaryRow, row.GetKey())
	assert.Equal(t, "BTC", restored.GetKey().GetSubjectId())
	assert.Equal(t, "binance", restored.GetKey().GetDimensions()["venue"])
	assert.NotContains(t, restored.GetAttributes(), timeSeriesDimensionsAttribute)
}

func TestRecordKeyAdapterRoundTrip(t *testing.T) {
	row := &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}

	primaryRow, err := recordRowToPrimaryStoreRow(row)
	require.NoError(t, err)
	assert.Equal(t, pb.DataKind_DATA_KIND_RECORD, primaryRow.GetKey().GetDataKind())

	restored := primaryStoreRowToRecordRow(primaryRow, row.GetKey())
	assert.Equal(t, "news-1", restored.GetKey().GetRecordId())
	assert.NotEmpty(t, restored.GetKey().GetVersion())
}

func TestValidateTimeRangeRejectsInvertedWindow(t *testing.T) {
	err := validateTimeRange(&pb.TimeRange{
		StartTime: "2026-07-11T00:00:00Z",
		EndTime:   "2026-07-10T00:00:00Z",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_time")
}

func TestValidateUserAttributesRejectsReservedPrefix(t *testing.T) {
	err := validateUserAttributes(map[string]string{reservedAttributePrefix + "secret": "x"})
	require.Error(t, err)
}

func TestPageMergedRecordRowsUsesSkippedTotal(t *testing.T) {
	rows := []*pb.RecordRow{
		{Key: &pb.RecordKey{RecordId: "a"}},
		{Key: &pb.RecordKey{RecordId: "b"}},
		{Key: &pb.RecordKey{RecordId: "c"}},
	}
	plan := &multiKeyPagePlan{pageNo: 2, size: 1, start: 1}
	paged, result := pageMergedRecordRows(rows, plan, false)

	require.Len(t, paged, 1)
	assert.Equal(t, "b", paged[0].GetKey().GetRecordId())
	assert.Equal(t, pb.TotalState_SKIPPED, result.GetTotalState())
	assert.True(t, result.GetHasMore())
}

func TestWriteTimeSeriesRowsRoutesAndPublishes(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	primaryStore := &capturingPrimary{}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   primaryStore,
		events:    bus,
	}

	var published int
	_, err := bus.SubscribeTimeSeriesRowsUpdated(ctx, func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		published++
		return nil
	})
	require.NoError(t, err)

	rsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}},
		}},
	}}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, 1, primaryStore.written)
	assert.Equal(t, 1, published)
}

func TestWriteRecordRowsAssignsTimestampVersion(t *testing.T) {
	ctx := context.Background()
	primaryStore := &capturingPrimary{}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{record: true}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   primaryStore,
		events:    eventbus.NewMemoryBus(),
	}

	rsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{
		Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"},
		Columns: []*pb.ColumnValue{{
			ColumnName: "title",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "headline"}},
		}},
	}}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetKeys(), 1)
	_, parseErr := time.Parse(time.RFC3339Nano, rsp.GetKeys()[0].GetVersion())
	assert.NoError(t, parseErr)
	assert.Equal(t, 1, primaryStore.written)
}

func TestErrorCodeHelpers(t *testing.T) {
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, primaryErrorCode(errors.New("field is required")))
	assert.Equal(t, pb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED, primaryErrorCode(errors.New("engine_capability_unsupported")))
	assert.Equal(t, pb.ErrorCode_ROUTE_NOT_FOUND, groupRowsErrorCode(errors.New("route missing")))
}

type writeValidatorMetadata struct {
	record bool
}

func (m writeValidatorMetadata) GetDataset(_ context.Context, spaceID, datasetID string) (*pb.Dataset, error) {
	kind := pb.DataKind_DATA_KIND_TIME_SERIES
	if m.record {
		kind = pb.DataKind_DATA_KIND_RECORD
	}
	return &pb.Dataset{
		SpaceId:   spaceID,
		DatasetId: datasetID,
		DataKind:  kind,
		Status:    "active",
	}, nil
}

func (writeValidatorMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return []*pb.DatasetColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	}, &pb.PageResult{}, nil
}

type capturingPrimary struct {
	written int
}

func (p *capturingPrimary) WriteRows(_ context.Context, _ *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	p.written += len(rows)
	return nil
}

func (*capturingPrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*capturingPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

var _ primary.Client = (*capturingPrimary)(nil)
