package test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestPersistentRestartAndDeliverySemantics exercises the same client path
// used by production services, but keeps all broker state in a temporary
// directory. It intentionally avoids scanning or touching a deployment data
// directory.
func TestPersistentRestartAndDeliverySemantics(t *testing.T) {
	cfg := testConfig(t)
	b := startBroker(t, cfg)
	client := connectClient(t, b.URL(), "e2e-first")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	kv, err := client.KeyValue("MOOX_CLOUDNODE_JOB_ACTIVE")
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	if _, err := kv.Create("job-1", []byte("active")); err != nil {
		t.Fatalf("KV Create() error = %v", err)
	}
	consumerCfg := jetstream.ConsumerConfig{Stream: "MOOX_STORAGE", Durable: "e2e-restart", FilterSubject: "moox.storage.>", AckWait: 200 * time.Millisecond, MaxDeliver: 5, MaxAckPending: 8, FetchMaxWait: time.Second}
	consumer, err := client.NewPullConsumer(ctx, consumerCfg)
	if err != nil {
		t.Fatalf("NewPullConsumer() error = %v", err)
	}
	msg := testMessage("e2e-storage-1", "moox.storage.time_series.rows_updated.v1", []byte("row"))
	ack, err := client.Publish(ctx, msg)
	if err != nil || ack == nil || ack.Stream != "MOOX_STORAGE" {
		t.Fatalf("Publish() ack=%+v err=%v", ack, err)
	}
	first, err := consumer.Fetch(ctx, 1)
	if err != nil || len(first) != 1 || first[0].Message.GetMessageId() != msg.GetMessageId() {
		t.Fatalf("first Fetch() deliveries=%v err=%v", first, err)
	}
	// Leave the delivery unacked. JetStream must redeliver it after the
	// process and broker restart, preserving the durable consumer position.
	_ = consumer.Close()
	_ = client.Close()
	if err := b.Shutdown(ctx); err != nil {
		t.Fatalf("first broker Shutdown() error = %v", err)
	}

	b = startBroker(t, cfg)
	defer b.Shutdown(context.Background())
	client = connectClient(t, b.URL(), "e2e-second")
	defer client.Close()
	consumer, err = client.NewPullConsumer(ctx, consumerCfg)
	if err != nil {
		t.Fatalf("recreated NewPullConsumer() error = %v", err)
	}
	defer consumer.Close()
	redelivered, err := fetchEventually(ctx, consumer, 1, 5*time.Second)
	if err != nil || len(redelivered) != 1 || redelivered[0].DeliveryCount < 2 {
		t.Fatalf("redelivery=%v err=%v, want durable redelivery", redelivered, err)
	}
	if err := redelivered[0].Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	restoredKV, err := client.KeyValue("MOOX_CLOUDNODE_JOB_ACTIVE")
	if err != nil {
		t.Fatalf("restored KeyValue() error = %v", err)
	}
	entry, err := restoredKV.Get("job-1")
	if err != nil || string(entry.Value()) != "active" {
		t.Fatalf("restored KV entry=%q err=%v", entry.Value(), err)
	}
}

func TestBoundedPublishBatchAndDLQStream(t *testing.T) {
	cfg := testConfig(t)
	// Keep this load test bounded to a temporary 256 MiB stream. The count is
	// large enough to exercise batch concurrency without becoming a production
	// data scan or an unbounded benchmark.
	for i := range cfg.Streams {
		if cfg.Streams[i].Name == "MOOX_METRICS" {
			cfg.Streams[i].MaxBytes = 256 << 20
		}
	}
	b := startBroker(t, cfg)
	defer b.Shutdown(context.Background())
	client := connectClient(t, b.URL(), "e2e-load")
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	consumer, err := client.NewPullConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_METRICS", Durable: "e2e-load-consumer", FilterSubject: "moox.metrics.>", AckWait: 5 * time.Second, MaxDeliver: 3, MaxAckPending: 1024, FetchMaxWait: time.Second})
	if err != nil {
		t.Fatalf("load consumer: %v", err)
	}
	defer consumer.Close()
	// Keep the batch large enough to exercise PublishBatch concurrency, but
	// small enough to finish under coverage instrumentation on slow hosts.
	const count = 2_000
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	messages := make([]*messagepb.MooxMessage, count)
	for i := range messages {
		messages[i] = testMessage("e2e-load-"+itoa(i), "moox.metrics.snapshot.reported.v1", payload)
	}
	started := time.Now()
	results := client.PublishBatch(ctx, messages)
	for i, result := range results {
		if result.Err != nil || result.Ack == nil {
			t.Fatalf("PublishBatch[%d] ack=%+v err=%v", i, result.Ack, result.Err)
		}
	}
	if elapsed := time.Since(started); elapsed > 45*time.Second {
		t.Fatalf("bounded PublishBatch took %s", elapsed)
	}
	var received int
	for received < count {
		batch, fetchErr := consumer.Fetch(ctx, 512)
		if fetchErr != nil {
			t.Fatalf("load Fetch() after %d messages: %v", received, fetchErr)
		}
		for _, delivery := range batch {
			if err := delivery.Ack(ctx); err != nil {
				t.Fatalf("load Ack() after %d messages: %v", received, err)
			}
			received++
		}
	}

	// A poison envelope is terminated by the consumer-facing client and a
	// valid, independently acknowledged message can still be published to DLQ.
	dlq, err := client.NewPullConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_DLQ", Durable: "e2e-dlq", FilterSubject: "moox.dlq.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, FetchMaxWait: time.Second})
	if err != nil {
		t.Fatalf("DLQ consumer: %v", err)
	}
	defer dlq.Close()
	nc, err := nats.Connect(b.URL())
	if err != nil {
		t.Fatal(err)
	}
	// Use a dedicated subject so DeliverAll cannot replay the preceding load
	// messages into the poison consumer.
	poison := &nats.Msg{Subject: "moox.metrics.poison.v1", Data: []byte("not protobuf"), Header: nats.Header{}}
	poison.Header.Set(nats.MsgIdHdr, "e2e-poison")
	poison.Header.Set("Content-Type", jetstream.OuterContentType)
	if err := nc.PublishMsg(poison); err != nil {
		t.Fatalf("publish poison: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	nc.Close()
	poisonConsumer, err := client.NewPullConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_METRICS", Durable: "e2e-poison", FilterSubject: "moox.metrics.poison.v1", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, FetchMaxWait: time.Second, DeliverDecodeErrors: true})
	if err != nil {
		t.Fatalf("poison consumer: %v", err)
	}
	defer poisonConsumer.Close()
	poisonDelivery, fetchErr := fetchEventually(ctx, poisonConsumer, 1, 5*time.Second)
	if fetchErr == nil || len(poisonDelivery) != 1 || poisonDelivery[0].Message != nil {
		t.Fatalf("poison delivery=%v err=%v, want decode error", poisonDelivery, fetchErr)
	}
	if err := poisonDelivery[0].Term(ctx); err != nil {
		t.Fatalf("Term() poison: %v", err)
	}
	dlqMessage := testMessage("e2e-poison.rejected", "moox.dlq.message.rejected.v1", []byte("poison"))
	if _, err := client.Publish(ctx, dlqMessage); err != nil {
		t.Fatalf("publish DLQ: %v", err)
	}
	dlqDelivery, err := fetchEventually(ctx, dlq, 1, 5*time.Second)
	if err != nil || len(dlqDelivery) != 1 || dlqDelivery[0].Message.GetTopic() != "moox.dlq.message.rejected.v1" {
		t.Fatalf("DLQ delivery=%v err=%v", dlqDelivery, err)
	}
	if err := dlqDelivery[0].Ack(ctx); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Broker.Host = "127.0.0.1"
	cfg.Broker.Port = freePort(t)
	cfg.Broker.ServerName = "eventbus-e2e"
	cfg.Broker.StoreDir = t.TempDir()
	cfg.Health.Addr = ""
	for i := range cfg.Streams {
		if cfg.Streams[i].MaxBytes > 0 {
			cfg.Streams[i].MaxBytes = 64 << 20
		}
	}
	return cfg
}

func startBroker(t *testing.T, cfg *config.Config) *broker.Server {
	t.Helper()
	b, err := broker.New(cfg)
	if err != nil {
		t.Fatalf("broker.New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("broker.Start() error = %v", err)
	}
	js, err := nats.Connect(b.URL())
	if err != nil {
		t.Fatalf("connect for registry: %v", err)
	}
	jetstreamCtx, err := js.JetStream()
	if err != nil {
		js.Close()
		t.Fatal(err)
	}
	reg, err := registry.New(jetstreamCtx, cfg)
	if err != nil {
		js.Close()
		t.Fatal(err)
	}
	if _, err := reg.Reconcile(ctx); err != nil {
		js.Close()
		_ = b.Shutdown(context.Background())
		t.Fatalf("registry.Reconcile() error = %v", err)
	}
	_ = js.Drain()
	js.Close()
	return b
}

func connectClient(t *testing.T, url, name string) *jetstream.Client {
	t.Helper()
	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{url}, Name: name, ConnectTimeout: 5 * time.Second, BatchConcurrency: 128})
	if err != nil {
		t.Fatalf("jetstream.Connect() error = %v", err)
	}
	return client
}

func fetchEventually(ctx context.Context, consumer *jetstream.PullConsumer, batch int, timeout time.Duration) ([]*jetstream.Delivery, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, time.Second)
		items, err := consumer.Fetch(fetchCtx, batch)
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

func testMessage(id, topic string, payload []byte) *messagepb.MooxMessage {
	now := timestamppb.Now()
	return &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: id, Topic: topic, Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, Producer: &messagepb.Producer{ServiceName: "e2e", InstanceId: "e2e-1", BootId: "boot-1"}, OccurredAt: now, PublishedAt: now, ContentType: "application/x-protobuf", Payload: payload}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
