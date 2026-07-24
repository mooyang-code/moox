package metrics

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

type fixedProducerAuthorizer struct {
	registered bool
	err        error
}

func (a fixedProducerAuthorizer) IsRegistered(context.Context, string, string) (bool, error) {
	return a.registered, a.err
}

func TestUnknownProducerTermsFirstDelivery(t *testing.T) {
	ctx, consumer, publisher, js := newMetricsEventBusConsumer(t, fixedProducerAuthorizer{})
	publishMetricReport(t, ctx, publisher, "unknown-producer")
	delivery := fetchMetricDelivery(t, ctx, consumer)

	result := consumer.Handle(ctx, delivery)
	if result.Decision != jetstream.TERM || result.Err == nil {
		t.Fatalf("Handle() = decision %v error %v, want TERM with diagnostic error", result.Decision, result.Err)
	}
	if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
		t.Fatal(err)
	}
	waitForMetricsConsumerState(t, js, func(info *nats.ConsumerInfo) bool {
		return info.NumPending == 0 && info.NumAckPending == 0
	})
}

func TestAuthorizerFailureRetriesAtMostThreeDeliveries(t *testing.T) {
	ctx, consumer, publisher, js := newMetricsEventBusConsumer(t, fixedProducerAuthorizer{err: errAuthorizerUnavailable})
	publishMetricReport(t, ctx, publisher, "authorizer-failure")

	for attempt := uint64(1); attempt <= 3; attempt++ {
		delivery := fetchMetricDelivery(t, ctx, consumer)
		if delivery.DeliveryCount != attempt {
			t.Fatalf("delivery count = %d, want %d", delivery.DeliveryCount, attempt)
		}
		result := consumer.Handle(ctx, delivery)
		if result.Decision != jetstream.RETRY || !errors.Is(result.Err, errAuthorizerUnavailable) {
			t.Fatalf("Handle() = decision %v error %v, want RETRY authorizer error", result.Decision, result.Err)
		}
		if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
			t.Fatal(err)
		}
	}

	info, err := js.ConsumerInfo("MOOX_METRICS", "monitor_metrics_ingest_v1")
	if err != nil || info.Config.MaxDeliver != 3 {
		t.Fatalf("consumer info = %+v, %v; want MaxDeliver=3", info, err)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	if deliveries, err := consumer.Fetch(fetchCtx, 1); len(deliveries) != 0 || (err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded)) {
		t.Fatalf("fourth Fetch() deliveries=%d err=%v, want no fourth delivery", len(deliveries), err)
	}
}

var errAuthorizerUnavailable = errors.New("authorizer unavailable")

func newMetricsEventBusConsumer(t *testing.T, authorizer ProducerAuthorizer) (context.Context, *Consumer, *events.Publisher, nats.JetStreamContext) {
	t.Helper()
	port := freeMetricsEventBusPort(t)
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		server.Shutdown()
		t.Fatal("test NATS server not ready")
	}
	t.Cleanup(server.Shutdown)

	control, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	js, err := control.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "MOOX_METRICS", Subjects: []string{"moox.metrics.>"},
		Retention: nats.LimitsPolicy, Storage: nats.MemoryStorage, Discard: nats.DiscardOld,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "monitor-metrics-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	consumer, err := NewConsumer(ctx, ConsumerOptions{
		Client: client, Storage: &StorageAdapter{}, MessageStore: &MetricMessageStore{}, Authorizer: authorizer,
		Config: monconfig.MetricsConfig{
			Stream: "MOOX_METRICS", Topic: MetricTopic, Consumer: "monitor_metrics_ingest_v1",
			FetchBatchSize: 1, FetchMaxWait: 200 * time.Millisecond, AckWait: 200 * time.Millisecond, MaxAckPending: 8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, consumer, publisher, js
}

func publishMetricReport(t *testing.T, ctx context.Context, publisher *events.Publisher, eventID string) {
	t.Helper()
	_, err := publisher.Publish(ctx, events.MetricsSnapshotReported, &metricspb.MetricReport{
		ServiceName: "fixture-service", InstanceId: "fixture-1",
		Snapshot: &metricspb.MetricSnapshot{
			SchemaVersion: 1, CollectionIntervalSeconds: 30,
			Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
			Data:   []byte("# TYPE fixture gauge\nfixture 1\n"),
		},
	}, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: InternalMetricSpaceID, SubjectID: "fixture-service/fixture-1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fetchMetricDelivery(t *testing.T, ctx context.Context, consumer *Consumer) *jetstream.Delivery {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fetchCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		deliveries, err := consumer.Fetch(fetchCtx, 1)
		cancel()
		if len(deliveries) > 0 {
			return deliveries[0]
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	t.Fatal("metric delivery was not available")
	return nil
}

func waitForMetricsConsumerState(t *testing.T, js nats.JetStreamContext, ready func(*nats.ConsumerInfo) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := js.ConsumerInfo("MOOX_METRICS", "monitor_metrics_ingest_v1")
		if err == nil && ready(info) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, err := js.ConsumerInfo("MOOX_METRICS", "monitor_metrics_ingest_v1")
	t.Fatalf("consumer state did not converge: info=%+v err=%v", info, err)
}

func freeMetricsEventBusPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
