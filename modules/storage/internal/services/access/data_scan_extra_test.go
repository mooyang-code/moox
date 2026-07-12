package access

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanRecordDatasetReturnsSortedRows(t *testing.T) {
	svc := &Service{
		primary: recordDatasetScanner{rowsPerPage: 2, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news"}},
		Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 2)
}

func TestReportViewErrorDelegatesToReporter(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	_, err := bus.SubscribeTimeSeriesRowsUpdated(ctx, func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		return errors.New("publish failed")
	})
	require.NoError(t, err)

	var reportedStage string
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   &capturingPrimary{},
		events:    bus,
		report: func(_ context.Context, stage string, err error) {
			reportedStage = stage
			require.Error(t, err)
		},
	}

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
	assert.Equal(t, "time_series_rows_updated", reportedStage)
}

func TestWriteRoutedRowsUsesMessageWriter(t *testing.T) {
	writer := &messageWriterPrimary{}
	svc := &Service{primary: writer}
	group := &routedRows{
		target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		rows:   []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline"}}},
	}
	require.NoError(t, svc.writeRoutedRows(context.Background(), group, []byte("outbox")))
	assert.True(t, writer.usedMessageWriter)
}

type recordDatasetScanner struct {
	rowsPerPage int
	pages       int
}

func (r recordDatasetScanner) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (r recordDatasetScanner) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (r recordDatasetScanner) ScanRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	if req.GetPage().GetCursor() != "" {
		_, _ = fmt.Sscanf(req.GetPage().GetCursor(), "%d", &pageNo)
	}
	rows := make([]*pb.PrimaryStoreRow, r.rowsPerPage)
	for i := range rows {
		rows[i] = &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
			SpaceId:   "crypto",
			DatasetId: "news",
			DataKind:  pb.DataKind_DATA_KIND_RECORD,
			Key:       fmt.Sprintf("news-%d", i),
			Version:   fmt.Sprintf("2026-07-09T00:00:%02dZ", i),
		}}
	}
	return rows, &pb.PageResult{
		HasMore:    int(pageNo) < r.pages,
		NextCursor: fmt.Sprint(pageNo + 1),
	}, nil
}

type messageWriterPrimary struct {
	usedMessageWriter bool
}

func (m *messageWriterPrimary) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (m *messageWriterPrimary) WriteRowsWithMessage(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow, []byte) error {
	m.usedMessageWriter = true
	return nil
}

func (*messageWriterPrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*messageWriterPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}
