package builder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestEnqueueTimeSeriesAcksAfterEnqueueWithoutWaitingForMaterialization(t *testing.T) {
	ctx := context.Background()
	batcher := newBatcher[timeSeriesDeriveItem](BatchOptions{BatchSize: 10, BatchWait: time.Hour})
	service := &Service{timeSeriesBatcher: batcher, runCtx: ctx}

	err := service.enqueueTimeSeries(ctx, &pb.TimeSeriesRowsUpdated{
		MessageId: "msg-1",
		SpaceId:   "crypto",
		DatasetId: "kline",
		Rows: []*pb.TimeSeriesRow{
			builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 1)),
		},
	})
	if err != nil {
		t.Fatalf("enqueueTimeSeries: %v", err)
	}

	select {
	case item := <-batcher.in:
		if item.journal.GetMessageId() != "msg-1" || len(item.journal.GetRows()) != 1 {
			t.Fatalf("queued item = %+v, want cloned journal rows", item.journal)
		}
	default:
		t.Fatalf("enqueueTimeSeries returned before placing journal in batcher")
	}
}

func TestProcessTimeSeriesBatchPatchesViewWithoutFactReader(t *testing.T) {
	ctx := context.Background()
	reader := &failingBuilderReader{}
	writer := &capturingTimeSeriesWriter{}
	service := &Service{
		reader:   reader,
		metadata: newBuilderServiceMetadata(builderTestView(), builderTestViewColumns()),
		views:    writer,
	}

	err := service.processTimeSeriesBatch(ctx, []*pb.TimeSeriesRow{
		builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 42)),
	})
	if err != nil {
		t.Fatalf("processTimeSeriesBatch: %v", err)
	}
	if reader.timeSeriesReads != 0 {
		t.Fatalf("fact reader calls = %d, want steady path to avoid PrimaryStore reread", reader.timeSeriesReads)
	}
	rows := writer.rows["active_spot_view"]
	if len(rows) != 1 {
		t.Fatalf("written rows = %d, want 1", len(rows))
	}
	if len(rows[0].GetColumns()) != 1 || rows[0].GetColumns()[0].GetColumnName() != "close" {
		t.Fatalf("written columns = %+v, want close patch only", rows[0].GetColumns())
	}
}

func TestProcessRecordBatchPatchesViewWithoutFactReader(t *testing.T) {
	ctx := context.Background()
	reader := &failingBuilderReader{}
	indexer := &capturingRecordIndexer{}
	service := &Service{
		reader:   reader,
		metadata: newBuilderServiceMetadata(builderTestRecordView(), builderTestViewColumns()),
		search:   indexer,
	}

	err := service.processRecordBatch(ctx, []*pb.RecordRow{
		builderTestRecordRow("crypto", "funding", "BTC-USDT", "v1", builderTestValue("rate", 0.03)),
	})
	if err != nil {
		t.Fatalf("processRecordBatch: %v", err)
	}
	if reader.recordReads != 0 {
		t.Fatalf("fact reader calls = %d, want steady path to avoid PrimaryStore reread", reader.recordReads)
	}
	rows := indexer.rows["active_spot_view"]
	if len(rows) != 1 {
		t.Fatalf("indexed rows = %d, want 1", len(rows))
	}
	if len(rows[0].GetColumns()) != 1 || rows[0].GetColumns()[0].GetColumnName() != "funding_rate" {
		t.Fatalf("indexed columns = %+v, want funding_rate patch only", rows[0].GetColumns())
	}
}

type builderServiceMetadata struct {
	view    *pb.View
	columns []*pb.ViewColumn
}

func newBuilderServiceMetadata(view *pb.View, columns []*pb.ViewColumn) *builderServiceMetadata {
	copied := proto.Clone(view).(*pb.View)
	copied.ActiveResult = "active_" + copied.GetViewId()
	copied.BuildingResult = "building_" + copied.GetViewId()
	return &builderServiceMetadata{view: copied, columns: columns}
}

func (m *builderServiceMetadata) GetView(context.Context, string, string) (*pb.View, error) {
	return proto.Clone(m.view).(*pb.View), nil
}

func (m *builderServiceMetadata) ListViews(context.Context, string, string, string, *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return []*pb.View{proto.Clone(m.view).(*pb.View)}, &pb.PageResult{}, nil
}

func (m *builderServiceMetadata) ListViewsByDataset(_ context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	if spaceID == m.view.GetSpaceId() {
		for _, candidate := range m.view.GetDatasetIds() {
			if candidate == datasetID {
				return []*pb.View{proto.Clone(m.view).(*pb.View)}, nil
			}
		}
		if datasetID == m.view.GetPrimaryDatasetId() {
			return []*pb.View{proto.Clone(m.view).(*pb.View)}, nil
		}
	}
	return nil, nil
}

func (m *builderServiceMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	out := make([]*pb.ViewColumn, 0, len(m.columns))
	for _, column := range m.columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out, &pb.PageResult{}, nil
}

func (m *builderServiceMetadata) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (m *builderServiceMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, nil
}

func (m *builderServiceMetadata) UpsertView(context.Context, *pb.View) (*pb.View, error) {
	return proto.Clone(m.view).(*pb.View), nil
}

func (m *builderServiceMetadata) BeginViewBuild(context.Context, string, string, uint64, string) (*pb.View, error) {
	return proto.Clone(m.view).(*pb.View), nil
}

func (m *builderServiceMetadata) CompleteViewBuild(context.Context, string, string, uint64, string) error {
	return nil
}

func (m *builderServiceMetadata) FailViewBuild(context.Context, string, string, uint64, string, error) error {
	return nil
}

type capturingTimeSeriesWriter struct {
	mu   sync.Mutex
	rows map[string][]*pb.TimeSeriesRow
}

type capturingRecordIndexer struct {
	mu   sync.Mutex
	rows map[string][]*pb.RecordRow
}

func (w *capturingRecordIndexer) IndexRecordViewRows(_ context.Context, resultName string, _ []*pb.ViewColumn, rows []*pb.RecordRow) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rows == nil {
		w.rows = make(map[string][]*pb.RecordRow)
	}
	for _, row := range rows {
		w.rows[resultName] = append(w.rows[resultName], proto.Clone(row).(*pb.RecordRow))
	}
	return nil
}

func (w *capturingTimeSeriesWriter) InsertRows(_ context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rows == nil {
		w.rows = make(map[string][]*pb.TimeSeriesRow)
	}
	for _, row := range rows {
		w.rows[tableName] = append(w.rows[tableName], proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return nil
}

type failingBuilderReader struct {
	timeSeriesReads int
	recordReads     int
}

func (r *failingBuilderReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.timeSeriesReads++
	return nil, errors.New("steady path must not read time-series facts")
}

func (r *failingBuilderReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	r.recordReads++
	return nil, errors.New("steady path must not read record facts")
}
