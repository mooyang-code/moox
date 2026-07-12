package test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	messagepb "github.com/mooyang-code/moox/packages/messagepb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

// TestEventBusToMonitorHistoryFlow covers the bounded production path without
// an HTTP scraper or a manually configured target: a reporter-shaped message
// is published to EventBus, Monitor consumes it, Storage receives rows, and
// the SQLite catalog/latest projection becomes queryable.
func TestEventBusToMonitorHistoryFlow(t *testing.T) {
	port := freeMetricsE2EPort(t)
	b, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go b.Start()
	if !b.ReadyForConnections(5 * time.Second) {
		b.Shutdown()
		t.Fatal("test NATS server not ready")
	}
	defer b.Shutdown()
	url := b.ClientURL()
	control, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	js, err := control.JetStream()
	if err != nil {
		control.Close()
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_METRICS", Subjects: []string{"moox.metrics.>"}, Retention: nats.LimitsPolicy, Storage: nats.FileStorage, MaxAge: 24 * time.Hour, MaxBytes: 32 << 20, Discard: nats.DiscardOld, Duplicates: 2 * time.Minute}); err != nil {
		control.Close()
		t.Fatal(err)
	}
	control.Close()

	eventClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{url}, Name: "monitor-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer eventClient.Close()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	access := &metricsE2EAccess{}
	storageCfg := monconfig.MetricsStorageConfig{SpaceID: metrics.InternalMetricSpaceID, DatasetID: "moox_service_metrics", Frequency: "30s", WriteBatchSize: 20}
	storage := metrics.NewStorageAdapter(access, nil, storageCfg)
	messageStore, err := store.WithDatabase(mgr, metrics.NewMetricMessageStore)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := metrics.NewConsumer(ctx, metrics.ConsumerOptions{Client: eventClient, Storage: storage, MessageStore: messageStore, ServiceName: "moox-monitor", InstanceID: "monitor-e2e", Config: monconfig.MetricsConfig{Stream: "MOOX_METRICS", Topic: metrics.MetricTopic, Consumer: "monitor-e2e-ingest", FetchBatchSize: 4, FetchMaxWait: time.Second, AckWait: time.Second, MaxAckPending: 8}})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	observed := time.Now().UTC().Truncate(time.Millisecond)
	raw := []byte("# HELP moox_e2e_requests Requests handled.\n# TYPE moox_e2e_requests counter\nmoox_e2e_requests{route=\"read\"} 7\n")
	snapshot := &metricspb.MetricSnapshot{SchemaVersion: 1, CollectionIntervalSeconds: 30, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT, Compression: metricspb.Compression_COMPRESSION_NONE, Data: raw, MetricFamilyCount: 1, SampleCount: 1}
	payload, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	now := timestamppb.New(observed)
	message := &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: "monitor-metric-e2e-1", Topic: metrics.MetricTopic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "fixture-service", InstanceId: "fixture-1", BootId: "boot-1", NodeId: "node-1", Version: "test"}, SpaceId: metrics.InternalMetricSpaceID, OccurredAt: now, PublishedAt: now, ContentType: metrics.MetricContentType, Payload: payload}
	if _, err := eventClient.Publish(ctx, message); err != nil {
		t.Fatal(err)
	}
	deliveries, err := fetchMetricsEventually(ctx, consumer, 5*time.Second)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("metrics Fetch() deliveries=%d err=%v", len(deliveries), err)
	}
	if err := consumer.HandleDelivery(ctx, deliveries[0]); err != nil {
		t.Fatal(err)
	}
	series, err := messageStore.ListSeries(ctx, "fixture-service", "moox_e2e_requests", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("catalog series=%d, want one discovered series", len(series))
	}
	latest, err := messageStore.GetLatest(ctx, series[0].SeriesID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Value != 7 || latest.MessageID != message.MessageId {
		t.Fatalf("latest=%+v, want value=7 and message=%q", latest, message.MessageId)
	}
	if len(access.rows) != 1 || access.rows[0].GetKey().GetSubjectId() != series[0].SeriesID {
		t.Fatalf("storage rows=%d row=%+v", len(access.rows), access.rows)
	}
}

type metricsE2EAccess struct {
	rows []*storagepb.TimeSeriesRow
}

func (a *metricsE2EAccess) WriteTimeSeriesRows(_ context.Context, req *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	a.rows = append(a.rows, req.GetRows()...)
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func (a *metricsE2EAccess) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func fetchMetricsEventually(ctx context.Context, consumer *metrics.Consumer, timeout time.Duration) ([]*jetstream.Delivery, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, time.Second)
		items, err := consumer.Fetch(fetchCtx, 1)
		cancel()
		if len(items) > 0 {
			return items, err
		}
		if err != nil && err != nats.ErrTimeout {
			return items, err
		}
		select {
		case <-deadline.C:
			return nil, context.DeadlineExceeded
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func freeMetricsE2EPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
