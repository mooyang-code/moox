package deriver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestServiceConsumesTimeSeriesRowsChangedAndWritesView(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	reader := &fakeDeriverReader{
		timeSeriesRows: []*pb.TimeSeriesRow{
			{
				Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
				Columns: []*pb.ColumnValue{doubleColumn("close", 8.1)},
			},
		},
	}
	meta := &fakeDeriverMetadata{
		viewsByDataset: map[string][]*pb.View{
			"crypto/kline": {
				{
					SpaceId:          "crypto",
					ViewId:           "kline_view",
					PrimaryDatasetId: "kline",
					Engine:           "duckdb",
					ActiveResult:     "ts_view_crypto_kline_active",
					BuildingResult:   "ts_view_crypto_kline_building",
				},
			},
		},
		columnsByView: map[string][]*pb.ViewColumn{
			"crypto/kline_view": {
				{
					ColumnName: "close_alias",
					OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					OriginId:   "kline.close",
					ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		},
	}
	writer := &capturingTimeSeriesWriter{}
	service := NewService(Options{
		Events:         bus,
		Reader:         reader,
		MetadataReader: meta,
		Views:          writer,
		Search:         &capturingRecordIndexer{},
		BatchSize:      1,
		BatchWait:      time.Millisecond,
		MaxWorkers:     1,
	})
	if err := service.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	err := bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"}},
	})
	if err != nil {
		t.Fatalf("PublishTimeSeriesRowsChanged error = %v", err)
	}

	eventually(t, time.Second, func() bool {
		return writer.callCount() == 2
	})
	calls := writer.calls()
	gotTables := map[string]bool{}
	for _, call := range calls {
		gotTables[call.tableName] = true
		if len(call.rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(call.rows))
		}
		columns := call.rows[0].GetColumns()
		if len(columns) != 1 || columns[0].GetColumnName() != "close_alias" {
			t.Fatalf("projected columns = %#v", columns)
		}
	}
	for _, tableName := range []string{"ts_view_crypto_kline_active", "ts_view_crypto_kline_building"} {
		if !gotTables[tableName] {
			t.Fatalf("missing write to %s, calls=%#v", tableName, calls)
		}
	}
}

func TestServiceTimeSeriesHandlerReturnsWriterError(t *testing.T) {
	ctx := context.Background()
	writerErr := errors.New("insert failed")
	service := newStartedDeriverTestService(t, ctx, deriverTestOptions{
		timeSeriesRows: []*pb.TimeSeriesRow{
			{
				Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
				Columns: []*pb.ColumnValue{doubleColumn("close", 8.1)},
			},
		},
		viewsByDataset: map[string][]*pb.View{
			"crypto/kline": {
				{
					SpaceId:          "crypto",
					ViewId:           "kline_view",
					PrimaryDatasetId: "kline",
					Engine:           "duckdb",
					ActiveResult:     "ts_view_crypto_kline_active",
				},
			},
		},
		columnsByView: map[string][]*pb.ViewColumn{
			"crypto/kline_view": {
				{
					ColumnName: "close_alias",
					OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					OriginId:   "kline.close",
					ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		},
		writerErr: writerErr,
	})

	err := service.enqueueTimeSeries(ctx, &pb.TimeSeriesRowsChangedEvent{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"}},
	})
	if !errors.Is(err, writerErr) {
		t.Fatalf("enqueueTimeSeries error = %v, want %v", err, writerErr)
	}
}

func TestServiceCloseFlushesPendingTimeSeriesHandler(t *testing.T) {
	ctx := context.Background()
	reader := &fakeDeriverReader{
		timeSeriesRows: []*pb.TimeSeriesRow{
			{
				Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
				Columns: []*pb.ColumnValue{doubleColumn("close", 8.1)},
			},
		},
	}
	meta := &fakeDeriverMetadata{
		viewsByDataset: map[string][]*pb.View{
			"crypto/kline": {
				{
					SpaceId:          "crypto",
					ViewId:           "kline_view",
					PrimaryDatasetId: "kline",
					Engine:           "duckdb",
					ActiveResult:     "ts_view_crypto_kline_active",
				},
			},
		},
		columnsByView: map[string][]*pb.ViewColumn{
			"crypto/kline_view": {
				{
					ColumnName: "close_alias",
					OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					OriginId:   "kline.close",
					ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		},
	}
	writer := &capturingTimeSeriesWriter{requireLiveContext: true}
	service := NewService(Options{
		Events:         eventbus.NewMemoryBus(),
		Reader:         reader,
		MetadataReader: meta,
		Views:          writer,
		Search:         &capturingRecordIndexer{},
		BatchSize:      10,
		BatchWait:      time.Hour,
		MaxWorkers:     1,
	})
	if err := service.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- service.enqueueTimeSeries(ctx, &pb.TimeSeriesRowsChangedEvent{
			Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"}},
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("enqueueTimeSeries returned before pending batch was closed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	if err := service.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("enqueueTimeSeries error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for enqueueTimeSeries after Close")
	}
	if got := writer.callCount(); got != 1 {
		t.Fatalf("writer call count = %d, want 1", got)
	}
}

func TestServiceConsumesRecordRowsChangedAndIndexesView(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	reader := &fakeDeriverReader{
		recordRows: []*pb.RecordRow{
			{
				Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "n1", Version: "v1"},
				Columns: []*pb.ColumnValue{stringColumn("title", "hello")},
			},
		},
	}
	meta := &fakeDeriverMetadata{
		viewsByDataset: map[string][]*pb.View{
			"crypto/news": {
				{
					SpaceId:          "crypto",
					ViewId:           "news_view",
					PrimaryDatasetId: "news",
					Engine:           "bleve",
					ActiveResult:     "record_view_crypto_news_active",
				},
			},
		},
		columnsByView: map[string][]*pb.ViewColumn{
			"crypto/news_view": {
				{
					ColumnName: "headline",
					OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					OriginId:   "news.title",
					ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
				},
			},
		},
	}
	indexer := &capturingRecordIndexer{}
	service := NewService(Options{
		Events:         bus,
		Reader:         reader,
		MetadataReader: meta,
		Views:          &capturingTimeSeriesWriter{},
		Search:         indexer,
		BatchSize:      1,
		BatchWait:      time.Millisecond,
		MaxWorkers:     1,
	})
	if err := service.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	err := bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news", RecordId: "n1", Version: "v1"}},
	})
	if err != nil {
		t.Fatalf("PublishRecordRowsChanged error = %v", err)
	}

	eventually(t, time.Second, func() bool {
		return indexer.callCount() == 1
	})
	calls := indexer.calls()
	if calls[0].resultName != "record_view_crypto_news_active" {
		t.Fatalf("resultName = %q, want record_view_crypto_news_active", calls[0].resultName)
	}
	if len(calls[0].rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(calls[0].rows))
	}
	columns := calls[0].rows[0].GetColumns()
	if len(columns) != 1 || columns[0].GetColumnName() != "headline" {
		t.Fatalf("projected columns = %#v", columns)
	}
}

func TestServiceRecordHandlerReturnsIndexerError(t *testing.T) {
	ctx := context.Background()
	indexErr := errors.New("index failed")
	service := newStartedDeriverTestService(t, ctx, deriverTestOptions{
		recordRows: []*pb.RecordRow{
			{
				Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "n1", Version: "v1"},
				Columns: []*pb.ColumnValue{stringColumn("title", "hello")},
			},
		},
		viewsByDataset: map[string][]*pb.View{
			"crypto/news": {
				{
					SpaceId:          "crypto",
					ViewId:           "news_view",
					PrimaryDatasetId: "news",
					Engine:           "bleve",
					ActiveResult:     "record_view_crypto_news_active",
				},
			},
		},
		columnsByView: map[string][]*pb.ViewColumn{
			"crypto/news_view": {
				{
					ColumnName: "headline",
					OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					OriginId:   "news.title",
					ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
				},
			},
		},
		indexErr: indexErr,
	})

	err := service.enqueueRecord(ctx, &pb.RecordRowsChangedEvent{
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news", RecordId: "n1", Version: "v1"}},
	})
	if !errors.Is(err, indexErr) {
		t.Fatalf("enqueueRecord error = %v, want %v", err, indexErr)
	}
}

func TestServiceStartRequiresSubscribableEventBus(t *testing.T) {
	service := NewService(Options{Events: publishOnlyBus{}})
	if err := service.Start(context.Background()); err == nil {
		t.Fatal("Start error = nil, want error")
	}
}

func TestServiceStartValidatesRequiredDependencies(t *testing.T) {
	base := func() Options {
		return Options{
			Events:         eventbus.NewMemoryBus(),
			Reader:         &fakeDeriverReader{},
			MetadataReader: &fakeDeriverMetadata{},
			Views:          &capturingTimeSeriesWriter{},
			Search:         &capturingRecordIndexer{},
		}
	}
	tests := []struct {
		name string
		mut  func(*Options)
	}{
		{name: "reader", mut: func(opts *Options) { opts.Reader = nil }},
		{name: "metadata reader", mut: func(opts *Options) { opts.MetadataReader = nil }},
		{name: "views", mut: func(opts *Options) { opts.Views = nil }},
		{name: "search", mut: func(opts *Options) { opts.Search = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := base()
			tt.mut(&opts)
			service := NewService(opts)
			if err := service.Start(context.Background()); err == nil {
				t.Fatal("Start error = nil, want error")
			}
		})
	}
}

type deriverTestOptions struct {
	timeSeriesRows []*pb.TimeSeriesRow
	recordRows     []*pb.RecordRow
	viewsByDataset map[string][]*pb.View
	columnsByView  map[string][]*pb.ViewColumn
	writerErr      error
	indexErr       error
}

func newStartedDeriverTestService(t *testing.T, ctx context.Context, opts deriverTestOptions) *Service {
	t.Helper()
	service := NewService(Options{
		Events: eventbus.NewMemoryBus(),
		Reader: &fakeDeriverReader{
			timeSeriesRows: opts.timeSeriesRows,
			recordRows:     opts.recordRows,
		},
		MetadataReader: &fakeDeriverMetadata{
			viewsByDataset: opts.viewsByDataset,
			columnsByView:  opts.columnsByView,
		},
		Views: &capturingTimeSeriesWriter{
			err: opts.writerErr,
		},
		Search: &capturingRecordIndexer{
			err: opts.indexErr,
		},
		BatchSize:  1,
		BatchWait:  time.Millisecond,
		MaxWorkers: 1,
	})
	if err := service.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

type fakeDeriverReader struct {
	timeSeriesRows []*pb.TimeSeriesRow
	recordRows     []*pb.RecordRow
}

func (r *fakeDeriverReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	rows := make([]*pb.TimeSeriesRow, 0, len(r.timeSeriesRows))
	for _, row := range r.timeSeriesRows {
		rows = append(rows, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

func (r *fakeDeriverReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	rows := make([]*pb.RecordRow, 0, len(r.recordRows))
	for _, row := range r.recordRows {
		rows = append(rows, proto.Clone(row).(*pb.RecordRow))
	}
	return &pb.ReadRecordRowsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type fakeDeriverMetadata struct {
	metadata.Store
	viewsByDataset map[string][]*pb.View
	columnsByView  map[string][]*pb.ViewColumn
}

func (m *fakeDeriverMetadata) ListViewsByDataset(_ context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	views := m.viewsByDataset[spaceID+"/"+datasetID]
	out := make([]*pb.View, 0, len(views))
	for _, item := range views {
		out = append(out, proto.Clone(item).(*pb.View))
	}
	return out, nil
}

func (m *fakeDeriverMetadata) ListViewColumns(_ context.Context, spaceID string, viewID string, _ *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	columns := m.columnsByView[spaceID+"/"+viewID]
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out, nil, nil
}

func (m *fakeDeriverMetadata) UpsertView(_ context.Context, item *pb.View) (*pb.View, error) {
	return proto.Clone(item).(*pb.View), nil
}

type timeSeriesWriteCall struct {
	tableName string
	rows      []*pb.TimeSeriesRow
}

type capturingTimeSeriesWriter struct {
	mu                 sync.Mutex
	items              []timeSeriesWriteCall
	err                error
	requireLiveContext bool
}

func (w *capturingTimeSeriesWriter) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	if w.requireLiveContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if w.err != nil {
		return w.err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	copied := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		copied = append(copied, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	w.items = append(w.items, timeSeriesWriteCall{tableName: tableName, rows: copied})
	return nil
}

func (w *capturingTimeSeriesWriter) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.items)
}

func (w *capturingTimeSeriesWriter) calls() []timeSeriesWriteCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]timeSeriesWriteCall, len(w.items))
	copy(out, w.items)
	return out
}

type recordIndexCall struct {
	resultName string
	columns    []*pb.ViewColumn
	rows       []*pb.RecordRow
}

type capturingRecordIndexer struct {
	mu    sync.Mutex
	items []recordIndexCall
	err   error
}

func (i *capturingRecordIndexer) IndexRecordViewRows(_ context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error {
	if i.err != nil {
		return i.err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	copiedColumns := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		copiedColumns = append(copiedColumns, proto.Clone(column).(*pb.ViewColumn))
	}
	copiedRows := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		copiedRows = append(copiedRows, proto.Clone(row).(*pb.RecordRow))
	}
	i.items = append(i.items, recordIndexCall{resultName: resultName, columns: copiedColumns, rows: copiedRows})
	return nil
}

func (i *capturingRecordIndexer) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.items)
}

func (i *capturingRecordIndexer) calls() []recordIndexCall {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]recordIndexCall, len(i.items))
	copy(out, i.items)
	return out
}

type publishOnlyBus struct{}

func (publishOnlyBus) PublishTimeSeriesRowsChanged(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
	return nil
}

func (publishOnlyBus) PublishRecordRowsChanged(context.Context, *pb.RecordRowsChangedEvent) error {
	return nil
}

func eventually(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if check() {
		return
	}
	t.Fatal("condition was not satisfied before timeout")
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
