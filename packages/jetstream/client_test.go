package jetstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPublishFetchAckAndHeaders(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()

	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")

	msg := validTestMessage("msg-1", "moox.test.events.v1")
	ack, err := client.Publish(context.Background(), msg)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if ack.Stream != "TEST" || ack.Sequence != 1 || ack.Duplicate {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	consumer, err := client.NewPullConsumer(context.Background(), ConsumerConfig{
		Stream:        "TEST",
		Durable:       "worker",
		FilterSubject: "moox.test.>",
		AckWait:       time.Second,
	})
	if err != nil {
		t.Fatalf("NewPullConsumer() error = %v", err)
	}
	defer consumer.Close()
	deliveries, err := consumer.Fetch(context.Background(), 1)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
	}
	d := deliveries[0]
	if d.Message.GetMessageId() != msg.GetMessageId() || d.Subject != msg.GetTopic() || d.Stream != "TEST" || d.Consumer != "worker" {
		t.Fatalf("unexpected delivery: %+v", d)
	}
	if d.StreamSeq != 1 || d.ConsumerSeq != 1 || d.DeliveryCount != 1 {
		t.Fatalf("unexpected delivery metadata: %+v", d)
	}
	if err := d.Ack(context.Background()); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}

	info, err := client.js.ConsumerInfo("TEST", "worker")
	if err != nil {
		t.Fatalf("ConsumerInfo() error = %v", err)
	}
	if info.NumAckPending != 0 {
		t.Fatalf("NumAckPending = %d, want 0", info.NumAckPending)
	}
}

func TestPublishDuplicateUsesMessageID(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")

	msg := validTestMessage("same-id", "moox.test.events.v1")
	first, err := client.Publish(context.Background(), msg)
	if err != nil {
		t.Fatalf("first Publish() error = %v", err)
	}
	second, err := client.Publish(context.Background(), msg)
	if err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	if first.Duplicate || !second.Duplicate || first.Sequence != second.Sequence {
		t.Fatalf("duplicate acks = %+v, %+v", first, second)
	}
}

func TestNakRedeliversDelivery(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer := newTestConsumer(t, client, "worker-nak", 2*time.Second)
	defer consumer.Close()

	if _, err := client.Publish(context.Background(), validTestMessage("nak", "moox.test.events.v1")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	first := fetchOne(t, consumer)
	if err := first.Nak(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("Nak() error = %v", err)
	}
	second := fetchOne(t, consumer)
	if second.DeliveryCount != 2 {
		t.Fatalf("DeliveryCount = %d, want 2", second.DeliveryCount)
	}
	if err := second.Ack(context.Background()); err != nil {
		t.Fatalf("Ack() redelivery error = %v", err)
	}
}

func TestAckWaitRedeliversUnackedDelivery(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer := newTestConsumer(t, client, "worker-wait", 30*time.Millisecond)
	defer consumer.Close()

	if _, err := client.Publish(context.Background(), validTestMessage("ack-wait", "moox.test.events.v1")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	first := fetchOne(t, consumer)
	time.Sleep(100 * time.Millisecond)
	second := fetchOne(t, consumer)
	if second.DeliveryCount < 2 {
		t.Fatalf("DeliveryCount = %d, want at least 2", second.DeliveryCount)
	}
	if err := second.Ack(context.Background()); err != nil {
		t.Fatalf("Ack() redelivery error = %v", err)
	}
	_ = first
}

func TestMalformedBodyIsRejected(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer := newTestConsumer(t, client, "worker-malformed", time.Second)
	defer consumer.Close()

	msg := &nats.Msg{Subject: "moox.test.bad.v1", Data: []byte("not protobuf"), Header: nats.Header{}}
	msg.Header.Set(nats.MsgIdHdr, "malformed")
	msg.Header.Set("Content-Type", OuterContentType)
	if err := client.nc.PublishMsg(msg); err != nil {
		t.Fatalf("raw PublishMsg() error = %v", err)
	}
	if err := client.nc.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if _, err := consumer.Fetch(context.Background(), 1); !errors.Is(err, ErrDecode) {
		t.Fatalf("Fetch() error = %v, want decode error", err)
	}
	if info, err := client.js.ConsumerInfo("TEST", "worker-malformed"); err != nil {
		t.Fatalf("ConsumerInfo() error = %v", err)
	} else if info.NumAckPending != 0 {
		t.Fatalf("malformed delivery remains pending: %d", info.NumAckPending)
	}
}

func TestFetchCanReturnMalformedDeliveryForDLQ(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer, err := client.NewPullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: "worker-malformed-dlq", FilterSubject: "moox.test.>", AckWait: time.Second, DeliverDecodeErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	msg := &nats.Msg{Subject: "moox.test.bad.v1", Data: []byte("not protobuf"), Header: nats.Header{}}
	msg.Header.Set(nats.MsgIdHdr, "malformed-dlq")
	msg.Header.Set("Content-Type", OuterContentType)
	if err := client.nc.PublishMsg(msg); err != nil {
		t.Fatal(err)
	}
	if err := client.nc.Flush(); err != nil {
		t.Fatal(err)
	}
	deliveries, err := consumer.Fetch(context.Background(), 1)
	if !errors.Is(err, ErrDecode) || len(deliveries) != 1 || deliveries[0].Message != nil || deliveries[0].RawMessageID != "malformed-dlq" {
		t.Fatalf("Fetch() deliveries=%v err=%v, want malformed delivery for DLQ", deliveries, err)
	}
	if err := deliveries[0].Term(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFetchReturnsDecodedDeliveriesWhenLaterMessageIsMalformed(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer := newTestConsumer(t, client, "worker-partial", time.Second)
	defer consumer.Close()
	valid := validTestMessage("partial-valid", "moox.test.events.v1")
	if _, err := client.Publish(context.Background(), valid); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	malformed := &nats.Msg{Subject: "moox.test.bad.v1", Data: []byte("not protobuf"), Header: nats.Header{}}
	malformed.Header.Set(nats.MsgIdHdr, "partial-malformed")
	malformed.Header.Set("Content-Type", OuterContentType)
	if err := client.nc.PublishMsg(malformed); err != nil {
		t.Fatalf("raw PublishMsg() error = %v", err)
	}
	if err := client.nc.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	later := validTestMessage("partial-later", "moox.test.events.v1")
	if _, err := client.Publish(context.Background(), later); err != nil {
		t.Fatalf("Publish() later error = %v", err)
	}
	deliveries, err := consumer.Fetch(context.Background(), 3)
	if !errors.Is(err, ErrDecode) || len(deliveries) != 2 || deliveries[0].Message.GetMessageId() != valid.GetMessageId() || deliveries[1].Message.GetMessageId() != later.GetMessageId() {
		t.Fatalf("Fetch() deliveries=%v err=%v, want both valid deliveries plus ErrDecode", deliveries, err)
	}
	for _, delivery := range deliveries {
		if err := delivery.Ack(context.Background()); err != nil {
			t.Fatalf("Ack() valid partial delivery error = %v", err)
		}
	}
}

func TestPublishBatchPreservesOrderAndPartialFailures(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")

	results := client.PublishBatch(context.Background(), []*messagepb.MooxMessage{
		validTestMessage("batch-ok", "moox.test.events.v1"),
		validTestMessage("batch-missing-stream", "moox.other.events.v1"),
		validTestMessage("batch-invalid", ""),
	})
	if len(results) != 3 {
		t.Fatalf("PublishBatch() len = %d, want 3", len(results))
	}
	if results[0].Err != nil || results[0].Ack == nil {
		t.Fatalf("first batch result = %+v", results[0])
	}
	if results[1].Err == nil || results[2].Err == nil {
		t.Fatalf("partial batch errors = %+v", results)
	}
}

func TestPublishBatchUsesBoundedConcurrencyForLargeInput(t *testing.T) {
	client := &Client{cfg: Config{BatchConcurrency: 8}}
	messages := make([]*messagepb.MooxMessage, 10000)
	for i := range messages {
		messages[i] = validTestMessage(fmt.Sprintf("large-%d", i), "")
	}
	results := client.PublishBatch(context.Background(), messages)
	if len(results) != len(messages) {
		t.Fatalf("PublishBatch() len = %d, want %d", len(results), len(messages))
	}
	for i, result := range results {
		if result.Ack != nil || !errors.Is(result.Err, ErrConnection) && !errors.Is(result.Err, ErrInvalidMessage) {
			t.Fatalf("result[%d] = %+v, want validation/connection error", i, result)
		}
	}
}

func TestClientReconnectsAfterBrokerRestart(t *testing.T) {
	port := reserveTestPort(t)
	storeDir := t.TempDir()
	srv := startTestServerAt(t, port, storeDir)
	client, err := Connect(context.Background(), Config{
		URLs:          []string{fmt.Sprintf("nats://127.0.0.1:%d", port)},
		Name:          "jetstream-reconnect-test",
		ReconnectWait: 20 * time.Millisecond,
		MaxReconnects: 100,
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()
	if _, err := client.js.AddStream(&nats.StreamConfig{Name: "TEST", Subjects: []string{"moox.test.>"}, Storage: nats.FileStorage, Retention: nats.LimitsPolicy}); err != nil {
		t.Fatalf("AddStream() error = %v", err)
	}

	srv.Shutdown()
	srv.WaitForShutdown()
	deadline := time.Now().Add(3 * time.Second)
	for client.nc.Status() != nats.RECONNECTING && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if client.nc.Status() != nats.RECONNECTING {
		t.Fatalf("client status = %v, want reconnecting", client.nc.Status())
	}
	srv = startTestServerAt(t, port, storeDir)
	defer srv.Shutdown()
	deadline = time.Now().Add(5 * time.Second)
	for !client.nc.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !client.nc.IsConnected() {
		t.Fatal("client did not reconnect")
	}
	if _, err := client.Publish(context.Background(), validTestMessage("after-reconnect", "moox.test.events.v1")); err != nil {
		t.Fatalf("Publish() after reconnect error = %v", err)
	}
}

func TestConnectRejectsUnavailableBroker(t *testing.T) {
	port := reserveTestPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := Connect(ctx, Config{URLs: []string{fmt.Sprintf("nats://127.0.0.1:%d", port)}, ConnectTimeout: 100 * time.Millisecond, MaxReconnects: 3})
	if client != nil || !errors.Is(err, ErrConnection) {
		t.Fatalf("Connect() client=%v err=%v, want ErrConnection", client, err)
	}
}

func TestConnectRejectsEmptyURLs(t *testing.T) {
	client, err := Connect(context.Background(), Config{})
	if client != nil || !errors.Is(err, ErrConnection) {
		t.Fatalf("Connect() client=%v err=%v, want ErrConnection", client, err)
	}
}

func connectTestClient(t *testing.T, url string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Connect(ctx, Config{URLs: []string{url}, Name: "jetstream-test", MaxPayload: 1024 * 1024})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return client
}

func ensureTestStream(t *testing.T, client *Client, name string, subject string) {
	t.Helper()
	if _, err := client.js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{subject}, Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy}); err != nil {
		t.Fatalf("AddStream() error = %v", err)
	}
}

func newTestConsumer(t *testing.T, client *Client, durable string, ackWait time.Duration) *PullConsumer {
	t.Helper()
	consumer, err := client.NewPullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: durable, FilterSubject: "moox.test.>", AckWait: ackWait, MaxDeliver: 5})
	if err != nil {
		t.Fatalf("NewPullConsumer() error = %v", err)
	}
	return consumer
}

func fetchOne(t *testing.T, consumer *PullConsumer) *Delivery {
	t.Helper()
	deliveries, err := consumer.Fetch(context.Background(), 1)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
	}
	return deliveries[0]
}

func validTestMessage(id, topic string) *messagepb.MooxMessage {
	now := timestamppb.Now()
	return &messagepb.MooxMessage{
		ProtocolVersion: 1,
		MessageId:       id,
		Topic:           topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer: &messagepb.Producer{
			ServiceName: "test-service",
			InstanceId:  "test-instance",
		},
		SpaceId:     "test",
		OccurredAt:  now,
		PublishedAt: now,
		ContentType: "application/json",
		Payload:     []byte(`{"ok":true}`),
		Attributes:  map[string]string{"test": "true"},
	}
}

func startTestServer(t *testing.T) (*natsserver.Server, string) {
	t.Helper()
	port := reserveTestPort(t)
	storeDir := t.TempDir()
	srv := startTestServerAt(t, port, storeDir)
	return srv, fmt.Sprintf("nats://127.0.0.1:%d", port)
}

func reserveTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

func startTestServerAt(t *testing.T, port int, storeDir string) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		t.Fatal("nats server not ready")
	}
	return srv
}
