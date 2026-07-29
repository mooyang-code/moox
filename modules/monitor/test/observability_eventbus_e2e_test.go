package test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	observabilityconsumer "github.com/mooyang-code/moox/modules/monitor/internal/observability/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUnifiedObservabilityDurableFailureAndRestartFlow(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{
		Name: events.ObservabilityStreamName(), Subjects: []string{events.ObservabilityFilterSubject},
		Storage: nats.MemoryStorage, Duplicates: 2 * time.Minute,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "observability-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	var metricCalls, hostCalls, healthCalls, retryCalls int
	routes := observabilityconsumer.Routes{
		Metrics: func(_ context.Context, message *eventpb.EventMessage, _ *metricspb.MetricReport) error {
			metricCalls++
			if message.GetEventId() == "metrics-retry" {
				retryCalls++
				if retryCalls == 1 {
					return errors.New("temporary metrics store outage")
				}
			}
			return nil
		},
		Host: func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error {
			hostCalls++
			return nil
		},
		Health: func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error {
			healthCalls++
			return nil
		},
	}
	cfg := observabilityconsumer.DefaultConfig()
	cfg.FetchMaxWait = 100 * time.Millisecond
	cfg.AckWait = 500 * time.Millisecond
	consumer, err := observabilityconsumer.NewConsumer(ctx, client, registry, cfg, routes)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	publishObservabilityMetric(t, ctx, publisher, "metrics-deduplicated")
	publishObservabilityMetric(t, ctx, publisher, "metrics-deduplicated")
	agentID := uuid.Must(uuid.NewV7()).String()
	if _, err := publisher.Publish(ctx, events.ObservabilityHostSnapshotReported, &hostmetricpb.HostMetric{
		AgentId: agentID, Hostname: "worker-a", Snapshot: &hostmetricpb.HostSnapshot{},
	}, events.PublishOptions{
		EventID: uuid.Must(uuid.NewV7()).String(), OccurredAt: time.Now().UTC(),
		SpaceID: "moox_system", SubjectID: agentID,
	}); err != nil {
		t.Fatal(err)
	}
	publishObservabilityHealth(t, ctx, publisher, "health-routed")

	for range 3 {
		delivery := fetchObservabilityDelivery(t, ctx, consumer)
		result := consumer.Handle(ctx, delivery)
		if result.Decision != jetstream.ACK {
			t.Fatalf("initial route decision=%v err=%v", result.Decision, result.Err)
		}
		if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
			t.Fatal(err)
		}
	}
	if metricCalls != 1 || hostCalls != 1 || healthCalls != 1 {
		t.Fatalf("deduplicated route calls metrics=%d host=%d health=%d", metricCalls, hostCalls, healthCalls)
	}

	publishObservabilityMetric(t, ctx, publisher, "metrics-retry")
	retryDelivery := fetchObservabilityDelivery(t, ctx, consumer)
	retryResult := consumer.Handle(ctx, retryDelivery)
	if retryResult.Decision != jetstream.RETRY {
		t.Fatalf("retry decision=%v err=%v", retryResult.Decision, retryResult.Err)
	}
	if err := jetstream.ApplyHandlerResult(ctx, retryDelivery, retryResult); err != nil {
		t.Fatal(err)
	}
	redelivery := fetchObservabilityDelivery(t, ctx, consumer)
	if redelivery.DeliveryCount != 2 {
		t.Fatalf("redelivery count=%d", redelivery.DeliveryCount)
	}
	if result := consumer.Handle(ctx, redelivery); result.Decision != jetstream.ACK {
		t.Fatalf("redelivery decision=%v err=%v", result.Decision, result.Err)
	} else if err := jetstream.ApplyHandlerResult(ctx, redelivery, result); err != nil {
		t.Fatal(err)
	}

	publishMalformedObservabilityHealth(t, server.JetStream())
	malformed := fetchObservabilityDelivery(t, ctx, consumer)
	term := consumer.Handle(ctx, malformed)
	if term.Decision != jetstream.TERM {
		t.Fatalf("malformed decision=%v err=%v", term.Decision, term.Err)
	}
	if err := jetstream.ApplyHandlerResult(ctx, malformed, term); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
	publishObservabilityHealth(t, ctx, publisher, "health-after-restart")
	restarted, err := observabilityconsumer.NewConsumer(ctx, client, registry, cfg, routes)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	resumed := fetchObservabilityDelivery(t, ctx, restarted)
	if result := restarted.Handle(ctx, resumed); result.Decision != jetstream.ACK {
		t.Fatalf("resumed decision=%v err=%v", result.Decision, result.Err)
	} else if err := jetstream.ApplyHandlerResult(ctx, resumed, result); err != nil {
		t.Fatal(err)
	}
	var durableNames []string
	for name := range server.JetStream().ConsumerNames(events.ObservabilityStreamName()) {
		durableNames = append(durableNames, name)
	}
	if len(durableNames) != 1 || durableNames[0] != observabilityconsumer.DefaultConsumer {
		t.Fatalf("durables=%v", durableNames)
	}
}

func publishObservabilityMetric(t *testing.T, ctx context.Context, publisher *events.Publisher, eventID string) {
	t.Helper()
	if _, err := publisher.Publish(ctx, events.ObservabilityMetricsSnapshotReported, &metricspb.MetricReport{
		ServiceName: "collector", InstanceId: "collector-1", NodeId: "node-a",
		Snapshot: &metricspb.MetricSnapshot{SchemaVersion: 1},
	}, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(),
		SpaceID: "moox_system", SubjectID: "collector/collector-1",
	}); err != nil {
		t.Fatal(err)
	}
}

func publishObservabilityHealth(t *testing.T, ctx context.Context, publisher *events.Publisher, eventID string) {
	t.Helper()
	if _, err := publisher.Publish(ctx, events.ObservabilityHealthCheckReported, &observabilitypb.HealthCheckReport{
		ObserverId: "scf-sentinel", CheckId: "monitor_ready", Kind: "http",
		Success: true, CheckedAt: timestamppb.Now(),
	}, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(),
		SpaceID: "moox_system", SubjectID: "scf-sentinel/monitor_ready",
	}); err != nil {
		t.Fatal(err)
	}
}

func publishMalformedObservabilityHealth(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	message := &eventpb.EventMessage{
		EventId: "health-malformed", EventName: events.ObservabilityHealthCheckReported.Name(),
		EventVersion: events.ObservabilityHealthCheckReported.Version(),
		SpaceId:      "moox_system", SubjectId: "bad", OccurredAt: timestamppb.Now(),
		Payload: []byte("not-protobuf"),
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	natsMessage := nats.NewMsg("moox.observability.health.check.reported.v1.moox_system.bad")
	natsMessage.Header.Set(nats.MsgIdHdr, message.GetEventId())
	natsMessage.Header.Set("Content-Type", events.ContentType)
	natsMessage.Data = raw
	if _, err := js.PublishMsg(natsMessage); err != nil {
		t.Fatal(err)
	}
}

func fetchObservabilityDelivery(t *testing.T, ctx context.Context, consumer *observabilityconsumer.Consumer) *jetstream.Delivery {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		fetchCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		deliveries, err := consumer.Fetch(fetchCtx, 1)
		cancel()
		if len(deliveries) > 0 {
			return deliveries[0]
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	t.Fatal("observability delivery unavailable")
	return nil
}
