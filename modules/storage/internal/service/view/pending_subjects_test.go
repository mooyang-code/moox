package view

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type pendingSubject struct {
	Space, View, Dataset string
	Row, Message         []byte
	Node, Store          string
	Sequence             uint64
	Contract, Primary    string
}

func TestFromScratchRebuildDoesNotJournalOrPublishSubjectReady(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, false)
	configureDatasetFreshnessView(svc, nil, "prices-next", "prices-next")
	building := svc.schemas["prices-next"]
	building.SchemaHash, building.ViewVersion = "schema", 1
	svc.schemas["prices-next"] = building
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	published := 0
	svc.readyPublisher = subjectPublisherFunc(func(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
		published++
		return &jetstream.PublishAck{}, nil
	})
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	payload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: "space", DatasetId: "market_prices", Rows: []*pb.RowFieldUpsert{row}})
	require.NoError(t, err)
	payload.SourceNodeId, payload.SourceStoreId, payload.SourceSequence = "node", "store", 1
	message := &eventpb.EventMessage{EventId: "source", SpaceId: "space", SubjectId: "market_prices", OccurredAt: timestamppb.Now()}
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	require.Equal(t, 1, engine.writes["prices-next"])
	_, err = os.ReadDir(svc.pendingSubjectsDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Zero(t, published)
}

func TestReplayDiscardsLegacyPendingSubjectsWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(svc, nil, "prices-index", "")
	schema := svc.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "schema", 1
	svc.schemas["prices-index"] = schema
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	require.NoError(t, os.MkdirAll(svc.pendingSubjectsDir, 0o700))
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	engine.rows = []*pb.RowFieldValues{{Key: row.Key, Fields: row.Fields}}
	require.NoError(t, writeLegacyPendingSubject(svc.pendingSubjectsDir, pendingSubject{
		Space: "space", View: "prices_view", Dataset: "market_prices",
		Node: "node", Store: "store", Sequence: 1, Contract: "schema:1", Primary: "market_prices",
		Row:     mustMarshalProto(t, &pb.RowFieldUpsert{Key: row.GetKey()}),
		Message: mustMarshalProto(t, &eventpb.EventMessage{EventId: "source", SpaceId: "space", SubjectId: "market_prices"}),
	}))
	published := 0
	svc.readyPublisher = subjectPublisherFunc(func(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
		published++
		return &jetstream.PublishAck{}, nil
	})
	require.NoError(t, svc.ReplayPendingSubjects(ctx))
	entries, err := os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Zero(t, published)
}

func TestStartEventConsumerDiscardsLegacyPendingSubjectsWithoutPublishing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	go server.Start()
	defer server.Shutdown()
	require.True(t, server.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(server.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: events.StorageViewConsumerStream, Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "pending-startup-test"))
	require.NoError(t, err)
	defer client.Close()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readySubject, err := registry.RenderSubject(events.ViewSourceSubjectReady, "space", "prices_view")
	require.NoError(t, err)
	sub, err := nc.SubscribeSync(readySubject)
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	engine.rows = []*pb.RowFieldValues{{Key: row.Key, Fields: row.Fields}}
	svc, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(svc, nil, "prices-index", "")
	schema := svc.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "schema", 1
	svc.schemas["prices-index"] = schema
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	require.NoError(t, os.MkdirAll(svc.pendingSubjectsDir, 0o700))
	require.NoError(t, writeLegacyPendingSubject(svc.pendingSubjectsDir, pendingSubject{
		Space: "space", View: "prices_view", Dataset: "market_prices",
		Node: "node", Store: "store", Sequence: 1, Contract: "schema:1", Primary: "market_prices",
		Row:     mustMarshalProto(t, &pb.RowFieldUpsert{Key: row.GetKey()}),
		Message: mustMarshalProto(t, &eventpb.EventMessage{EventId: "restart-source", SpaceId: "space", SubjectId: "market_prices"}),
	}))
	filter, err := registry.RenderSubject(events.DatasetRowsUpserted, "space", "market_prices")
	require.NoError(t, err)
	stop, err := svc.StartEventConsumer(ctx, client, EventConsumerOptions{Consumer: events.StorageViewKlineConsumer, FilterSubjects: []string{filter}, FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1})
	require.NoError(t, err)
	defer stop()
	_, err = sub.NextMsg(300 * time.Millisecond)
	require.Error(t, err, "rebuild leftover journal must not emit subject-ready")
	entries, err := os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func writeLegacyPendingSubject(dir string, pending pendingSubject) error {
	data, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "legacy.json"), data, 0o600)
}

func mustMarshalProto(t *testing.T, message proto.Message) []byte {
	t.Helper()
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	return data
}

func TestSystemMetricsFromScratchDoesNotJournalPendingSubjects(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, false)
	key := viewRef{spaceID: "mooxsys", viewID: "view_mooxsys_service_metrics"}
	indexID := "metrics-next"
	svc.views[key] = &viewRuntime{active: "", next: indexID, status: "building"}
	svc.indexView = map[string]viewRef{indexID: key}
	svc.indexEngine[indexID] = "query-test"
	svc.schemas[indexID] = viewindex.ViewIndexSchema{
		SpaceID: "mooxsys", ViewID: "view_mooxsys_service_metrics", PrimaryDatasetID: "dataset_mooxsys_service_metrics",
		SchemaHash: "schema", ViewVersion: 1,
		Columns: []*pb.ViewColumn{{OriginId: "dataset_mooxsys_service_metrics.cpu", ColumnName: "cpu"}},
	}
	svc.byData = map[datasetRef]map[string]struct{}{{spaceID: "mooxsys", datasetID: "dataset_mooxsys_service_metrics"}: {indexID: {}}}
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	published := 0
	svc.readyPublisher = subjectPublisherFunc(func(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
		published++
		return &jetstream.PublishAck{}, nil
	})
	row := &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: "mooxsys", DatasetId: "dataset_mooxsys_service_metrics", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "collector", Freq: "1m", SeriesTag: "host", DataTime: "2026-09-13T08:00:00Z",
		}}},
		Fields: []*pb.FieldValue{{FieldId: "cpu", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
	payload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: "mooxsys", DatasetId: "dataset_mooxsys_service_metrics", Rows: []*pb.RowFieldUpsert{row}})
	require.NoError(t, err)
	payload.SourceNodeId, payload.SourceStoreId, payload.SourceSequence = "node", "store", 1
	message := &eventpb.EventMessage{EventId: "source", SpaceId: "mooxsys", SubjectId: "dataset_mooxsys_service_metrics", OccurredAt: timestamppb.Now()}
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	require.Equal(t, 1, engine.writes[indexID])
	_, err = os.ReadDir(svc.pendingSubjectsDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Zero(t, published)
}

func TestReplayDropsPendingSubjectsForSystemViews(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, false)
	key := viewRef{spaceID: "mooxsys", viewID: "view_mooxsys_service_metrics"}
	svc.views[key] = &viewRuntime{active: "", next: "metrics-next", status: "building"}
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	require.NoError(t, os.MkdirAll(svc.pendingSubjectsDir, 0o700))
	row := &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: "mooxsys", DatasetId: "dataset_mooxsys_service_metrics", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "collector", Freq: "1m", SeriesTag: "host", DataTime: "2026-09-13T08:00:00Z",
		}}},
	}
	rowData, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.RowFieldUpsert{Key: row.GetKey()})
	require.NoError(t, err)
	messageData, err := proto.MarshalOptions{Deterministic: true}.Marshal(&eventpb.EventMessage{EventId: "source", SpaceId: "mooxsys", SubjectId: "dataset_mooxsys_service_metrics"})
	require.NoError(t, err)
	data, err := json.Marshal(pendingSubject{
		Space: "mooxsys", View: "view_mooxsys_service_metrics", Dataset: "dataset_mooxsys_service_metrics",
		Row: rowData, Message: messageData, Node: "node", Store: "store", Sequence: 1,
		Contract: "schema:1", Primary: "dataset_mooxsys_service_metrics",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(svc.pendingSubjectsDir, "metrics.json"), data, 0o600))
	published := 0
	svc.readyPublisher = subjectPublisherFunc(func(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
		published++
		return &jetstream.PublishAck{}, nil
	})
	require.NoError(t, svc.ReplayPendingSubjects(ctx))
	entries, err := os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Zero(t, published)
}
