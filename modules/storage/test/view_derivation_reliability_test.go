//go:build cgo

package test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	viewsearch "github.com/mooyang-code/moox/modules/storage/internal/service/dataview/search"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder"
	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

func TestViewDerivationReliabilityRetriesDuckDBAndBleveBeforeAck(t *testing.T) {
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddStream(&nats.StreamConfig{
		Name: "MOOX_STORAGE", Subjects: []string{
			infraeventbus.DefaultTimeSeriesRowsCommittedSubject,
			infraeventbus.DefaultRecordRowsCommittedSubject,
		}, Storage: nats.FileStorage,
	})
	if err != nil {
		t.Fatal(err)
	}
	const ackWait = 3 * time.Second
	const durable = "storage_view_rows_committed_v1"
	if _, err := js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{
		Name: durable, Durable: durable, FilterSubject: infraeventbus.RowsCommittedSubjectWildcard(infraeventbus.DefaultSubjectPrefix),
		AckPolicy: nats.AckExplicitPolicy, AckWait: ackWait, MaxDeliver: -1, MaxAckPending: 128,
	}); err != nil {
		t.Fatal(err)
	}

	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv([]string{ns.ClientURL()}, "storage-view-reliability-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	bus, err := infraeventbus.NewSubscriberBus(client, infraeventbus.DefaultSubjectPrefix, infraeventbus.SubscriberOptions{
		StreamName: "MOOX_STORAGE", AckWait: ackWait, MaxDeliver: -1, MaxInFlight: 4, MaxAckPending: 128,
		NakDelay: 20 * time.Millisecond, ActionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	root := t.TempDir()
	duck, err := duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = duck.Close() })
	bleve := viewsearch.NewService(viewsearch.Options{Root: root})
	t.Cleanup(func() { _ = bleve.Close() })

	columns := []*pb.ViewColumn{{
		ColumnName: "value", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}
	duckIndex := viewindex.ViewIndexID("crypto", "spot_view", viewindex.SlotA)
	bleveIndex := viewindex.ViewIndexID("crypto", "news_view", viewindex.SlotA)
	for _, prepared := range []struct {
		engine viewindex.ViewIndexEngine
		id     string
		viewID string
		name   string
	}{
		{duck, duckIndex, "spot_view", "duckdb"},
		{bleve, bleveIndex, "news_view", "bleve"},
	} {
		if err := prepared.engine.Prepare(context.Background(), prepared.id, viewindex.ViewIndexSchema{
			SpaceID: "crypto", ViewID: prepared.viewID, Engine: prepared.name, Columns: columns, ViewVersion: 1,
		}); err != nil {
			t.Fatalf("prepare %s: %v", prepared.name, err)
		}
	}
	if err := bleve.Apply(context.Background(), bleveIndex, viewindex.ViewIndexApplyBatch{
		ViewVersion:       1,
		CheckpointUpdates: []viewindex.ShardCheckpointUpdate{{ShardID: "shard-1", LastAppliedSequence: 1}},
	}); err != nil {
		t.Fatalf("seed bleve checkpoint: %v", err)
	}

	duckFailOnce := &failOnceEngine{ViewIndexEngine: duck}
	bleveFailOnce := &failOnceEngine{ViewIndexEngine: bleve}
	metadata := &e2eViewMetadata{columns: columns, views: []*pb.View{
		activeE2EView("crypto", "spot", "spot_view", "duckdb", duckIndex, columns),
		activeE2EView("crypto", "news", "news_view", "bleve", bleveIndex, columns),
	}}
	service := builder.NewService(builder.Options{
		Events: bus, Reader: emptyFactReader{}, Metadata: metadata,
		Engines:   map[string]viewindex.ViewIndexEngine{"duckdb": duckFailOnce, "bleve": bleveFailOnce},
		BatchSize: 1, BatchWait: 10 * time.Millisecond, MaxWorkers: 2,
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	timeRow := &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "spot", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-17T00:00:00Z"},
		Columns: []*pb.ColumnValue{{ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: doubleValue(1)}},
	}
	if err := bus.PublishTimeSeriesRowsCommitted(context.Background(), &pb.TimeSeriesRowsCommitted{
		ShardId: "shard-1", SpaceId: "crypto", DatasetId: "spot", Sequence: 1, Writes: []*pb.TimeSeriesRowWrite{{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: timeRow}},
	}); err != nil {
		t.Fatal(err)
	}
	recordRow := &pb.RecordRow{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "2026-07-17T00:00:00Z"},
		Columns: []*pb.ColumnValue{{ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: doubleValue(2)}},
	}
	if err := bus.PublishRecordRowsCommitted(context.Background(), &pb.RecordRowsCommitted{
		ShardId: "shard-1", SpaceId: "crypto", DatasetId: "news", Sequence: 2, Writes: []*pb.RecordRowWrite{{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: recordRow}},
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool {
		duckStats, duckErr := duck.Stat(context.Background(), duckIndex)
		bleveStats, bleveErr := bleve.Stat(context.Background(), bleveIndex)
		return duckErr == nil && bleveErr == nil && duckStats.EntryCount == 1 && bleveStats.EntryCount == 1 &&
			duckFailOnce.Attempts() >= 2 && bleveFailOnce.Attempts() >= 2
	})
	waitFor(t, 5*time.Second, func() bool {
		info, infoErr := js.ConsumerInfo("MOOX_STORAGE", durable)
		return infoErr == nil && info.NumAckPending == 0 && info.NumPending == 0
	})

	duckStats, _ := duck.Stat(context.Background(), duckIndex)
	bleveStats, _ := bleve.Stat(context.Background(), bleveIndex)
	if duckStats.EntryCount != 1 || bleveStats.EntryCount != 1 {
		t.Fatalf("replayed derived counts duckdb=%d bleve=%d, want 1/1", duckStats.EntryCount, bleveStats.EntryCount)
	}
}

type failOnceEngine struct {
	viewindex.ViewIndexEngine
	mu       sync.Mutex
	attempts int
}

func (e *failOnceEngine) Apply(ctx context.Context, indexID string, batch viewindex.ViewIndexApplyBatch) error {
	applier, ok := e.ViewIndexEngine.(viewindex.ViewIndexApplier)
	if !ok {
		return errors.New("test engine does not support atomic apply")
	}
	e.mu.Lock()
	e.attempts++
	attempt := e.attempts
	e.mu.Unlock()
	if attempt == 1 {
		if err := applier.Apply(ctx, indexID, batch); err != nil {
			return err
		}
		return errors.New("injected post-write acknowledgement failure")
	}
	return applier.Apply(ctx, indexID, batch)
}

func (e *failOnceEngine) Attempts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts
}

type e2eViewMetadata struct {
	views   []*pb.View
	columns []*pb.ViewColumn
}

func (m *e2eViewMetadata) ListViews(_ context.Context, spaceID, datasetID, status string, _ *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	var out []*pb.View
	for _, view := range m.views {
		if (spaceID == "" || view.GetSpaceId() == spaceID) &&
			(datasetID == "" || view.GetPrimaryDatasetId() == datasetID) &&
			(status == "" || view.GetStatus() == status) {
			out = append(out, proto.Clone(view).(*pb.View))
		}
	}
	return out, &pb.PageResult{}, nil
}

func (m *e2eViewMetadata) ListViewsByDataset(_ context.Context, spaceID, datasetID string) ([]*pb.View, error) {
	var out []*pb.View
	for _, view := range m.views {
		if view.GetSpaceId() == spaceID && view.GetPrimaryDatasetId() == datasetID {
			out = append(out, proto.Clone(view).(*pb.View))
		}
	}
	return out, nil
}

func (m *e2eViewMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return cloneE2EColumns(m.columns), &pb.PageResult{}, nil
}

type emptyFactReader struct{}

func (emptyFactReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (emptyFactReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (emptyFactReader) ScanTimeSeriesRows(context.Context, string, string, *pb.TimeRange, []string, *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (emptyFactReader) ScanRecordRows(context.Context, string, string, *pb.VersionRange, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func activeE2EView(spaceID, datasetID, viewID, engine, indexID string, columns []*pb.ViewColumn) *pb.View {
	return &pb.View{
		SpaceId: spaceID, ViewId: viewID, PrimaryDatasetId: datasetID, DatasetIds: []string{datasetID},
		Engine: engine, Status: "active", ViewVersion: 1, ActiveViewVersion: 1,
		ActiveIndexId: indexID, ActiveColumns: cloneE2EColumns(columns),
	}
}

func cloneE2EColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out
}

func doubleValue(value float64) *pb.TypedValue {
	return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
