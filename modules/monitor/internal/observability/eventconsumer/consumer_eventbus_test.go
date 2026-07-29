package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUnifiedObservabilityConsumerRoutesAllEvents(t *testing.T) {
	var metricCalls, hostCalls, healthCalls atomic.Int32
	ctx, consumer, publisher, js := newObservabilityFixture(t, Routes{
		Metrics: func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error {
			metricCalls.Add(1)
			return nil
		},
		Host: func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error {
			hostCalls.Add(1)
			return nil
		},
		Health: func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error {
			healthCalls.Add(1)
			return nil
		},
	})
	publishAllObservabilityEvents(t, ctx, publisher)

	seen := map[string]bool{}
	for len(seen) < 3 {
		delivery := fetchDelivery(t, ctx, consumer)
		result := consumer.Handle(ctx, delivery)
		if result.Decision != jetstream.ACK {
			t.Fatalf("Handle(%s) = %v, %v", delivery.Subject, result.Decision, result.Err)
		}
		if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
			t.Fatal(err)
		}
		seen[routeFromSubject(delivery.Subject)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("routes = %v", seen)
	}
	if metricCalls.Load() != 1 || hostCalls.Load() != 1 || healthCalls.Load() != 1 {
		t.Fatalf("route calls metrics=%d host=%d health=%d", metricCalls.Load(), hostCalls.Load(), healthCalls.Load())
	}
	info, err := js.ConsumerInfo(events.ObservabilityStreamName(), DefaultConsumer)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.Durable != DefaultConsumer || info.Config.FilterSubject != events.ObservabilityFilterSubject {
		t.Fatalf("consumer config = %+v", info.Config)
	}
}

func TestUnifiedObservabilityConsumerTermsInvalidAndUnknownEvents(t *testing.T) {
	ctx, consumer, _, js := newObservabilityFixture(t, noopRoutes())
	before := testutil.ToFloat64(rejectedEvents)
	publishRawEvent(t, js, "moox.observability.health.check.reported.v1.moox_system.bad", &eventpb.EventMessage{
		EventId: "invalid-health", EventName: events.ObservabilityHealthCheckReported.Name(),
		EventVersion: events.ObservabilityHealthCheckReported.Version(), SpaceId: "moox_system",
		SubjectId: "bad", OccurredAt: timestamppb.Now(), Payload: []byte("not-protobuf"),
	})
	publishRawEvent(t, js, "moox.observability.unknown.reported.v1.moox_system.bad", &eventpb.EventMessage{
		EventId: "unknown-event", EventName: "observability.unknown.reported", EventVersion: 1,
		SpaceId: "moox_system", SubjectId: "bad", OccurredAt: timestamppb.Now(), Payload: []byte{1},
	})
	for range 2 {
		delivery := fetchDelivery(t, ctx, consumer)
		result := consumer.Handle(ctx, delivery)
		if result.Decision != jetstream.TERM || result.Err == nil {
			t.Fatalf("Handle() = %v, %v; want TERM", result.Decision, result.Err)
		}
		if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(rejectedEvents); got < before+2 {
		t.Fatalf("rejected total = %v, want at least %v", got, before+2)
	}
}

func TestUnifiedObservabilityConsumerNaksTransientRouteFailure(t *testing.T) {
	transient := errors.New("storage temporarily unavailable")
	ctx, consumer, publisher, _ := newObservabilityFixture(t, Routes{
		Metrics: func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error { return transient },
		Host:    countHostRoute(t, "host"),
		Health:  countHealthRoute(t, "health"),
	})
	publishMetric(t, ctx, publisher, "metric-transient")
	delivery := fetchDelivery(t, ctx, consumer)
	result := consumer.Handle(ctx, delivery)
	if result.Decision != jetstream.RETRY || !errors.Is(result.Err, transient) {
		t.Fatalf("Handle() = %v, %v; want RETRY", result.Decision, result.Err)
	}
	if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
		t.Fatal(err)
	}
	redelivery := fetchDelivery(t, ctx, consumer)
	if redelivery.DeliveryCount != 2 {
		t.Fatalf("redelivery count = %d, want 2 after NAK", redelivery.DeliveryCount)
	}
}

func TestUnifiedObservabilityConsumerReusesDurableAfterRestart(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: events.ObservabilityStreamName(), Subjects: []string{events.ObservabilityFilterSubject}, Storage: nats.MemoryStorage})
	ctx := context.Background()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "monitor-observability-restart"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewConsumer(ctx, client, registry, DefaultConfig(), noopRoutes())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewConsumer(ctx, client, registry, DefaultConfig(), noopRoutes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	names := server.JetStream().ConsumerNames(events.ObservabilityStreamName())
	var got []string
	for name := range names {
		got = append(got, name)
	}
	if len(got) != 1 || got[0] != DefaultConsumer {
		t.Fatalf("consumer names = %v", got)
	}
}

func TestUnifiedObservabilityConsumerSignalsReadyOnFirstReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	consumer, _, _ := newObservabilityFixtureWithContext(t, ctx, noopRoutes())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(ctx, func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not enter receive state")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not stop")
	}
}

func TestUnifiedObservabilityConsumerFailsBeforeReadyWhenStreamMissing(t *testing.T) {
	server := testkit.Start(t)
	ctx := context.Background()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "monitor-observability-missing-stream"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewConsumer(ctx, client, registry, DefaultConfig(), noopRoutes()); err == nil {
		t.Fatal("NewConsumer succeeded without the observability stream")
	}
}

func newObservabilityFixture(t *testing.T, routes Routes) (context.Context, *Consumer, *events.Publisher, nats.JetStreamContext) {
	t.Helper()
	ctx := context.Background()
	consumer, publisher, js := newObservabilityFixtureWithContext(t, ctx, routes)
	return ctx, consumer, publisher, js
}

func newObservabilityFixtureWithContext(t *testing.T, ctx context.Context, routes Routes) (*Consumer, *events.Publisher, nats.JetStreamContext) {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{Name: events.ObservabilityStreamName(), Subjects: []string{events.ObservabilityFilterSubject}, Storage: nats.MemoryStorage})
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "monitor-observability-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewConsumer(ctx, client, registry, DefaultConfig(), routes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	return consumer, publisher, server.JetStream()
}

func noopRoutes() Routes {
	return Routes{
		Metrics: func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error { return nil },
		Host:    func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error { return nil },
		Health:  func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error { return nil },
	}
}

func publishAllObservabilityEvents(t *testing.T, ctx context.Context, publisher *events.Publisher) {
	t.Helper()
	publishMetric(t, ctx, publisher, "metric-route")
	agentID := uuid.Must(uuid.NewV7()).String()
	_, err := publisher.Publish(ctx, events.ObservabilityHostSnapshotReported, &hostmetricpb.HostMetric{
		AgentId: agentID, Hostname: "host-a", Snapshot: &hostmetricpb.HostSnapshot{},
	}, events.PublishOptions{EventID: uuid.Must(uuid.NewV7()).String(), OccurredAt: time.Now().UTC(), SpaceID: "moox_system", SubjectID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Publish(ctx, events.ObservabilityHealthCheckReported, &observabilitypb.HealthCheckReport{
		ObserverId: "monitor", CheckId: "gateway-ready", Kind: "trpc", Success: true, CheckedAt: timestamppb.Now(),
	}, events.PublishOptions{EventID: "health-route", OccurredAt: time.Now().UTC(), SpaceID: "moox_system", SubjectID: "monitor/gateway-ready"})
	if err != nil {
		t.Fatal(err)
	}
}

func publishMetric(t *testing.T, ctx context.Context, publisher *events.Publisher, eventID string) {
	t.Helper()
	_, err := publisher.Publish(ctx, events.ObservabilityMetricsSnapshotReported, &metricspb.MetricReport{
		ServiceName: "collector", InstanceId: "collector-1", NodeId: "node-a",
		Snapshot: &metricspb.MetricSnapshot{SchemaVersion: 1},
	}, events.PublishOptions{EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: "moox_system", SubjectID: "collector/collector-1"})
	if err != nil {
		t.Fatal(err)
	}
}

func publishRawEvent(t *testing.T, js nats.JetStreamContext, subject string, message *eventpb.EventMessage) {
	t.Helper()
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	msg := nats.NewMsg(subject)
	msg.Header.Set(nats.MsgIdHdr, message.GetEventId())
	msg.Header.Set("Content-Type", events.ContentType)
	msg.Data = raw
	if _, err := js.PublishMsg(msg); err != nil {
		t.Fatal(err)
	}
}

func fetchDelivery(t *testing.T, ctx context.Context, consumer *Consumer) *jetstream.Delivery {
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
	t.Fatal("delivery unavailable")
	return nil
}

func countHostRoute(t *testing.T, name string) func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error {
	t.Helper()
	return func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error { return nil }
}

func countHealthRoute(t *testing.T, name string) func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error {
	t.Helper()
	return func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error { return nil }
}

func routeFromSubject(subject string) string {
	switch {
	case contains(subject, ".metrics."):
		return "metrics"
	case contains(subject, ".host."):
		return "host"
	case contains(subject, ".health."):
		return "health"
	default:
		return fmt.Sprintf("unknown:%s", subject)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
