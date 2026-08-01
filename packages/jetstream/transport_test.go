package jetstream

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestPublishRawAndFetchLeavesBusinessDecodingToCaller(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer := newTestConsumer(t, client, "raw", time.Second)
	defer consumer.Close()
	if _, err := client.PublishRaw(context.Background(), "moox.test.raw.v1", "raw-1", []byte("not decoded here"), "application/vnd.moox.event+protobuf"); err != nil {
		t.Fatal(err)
	}
	delivery := fetchOne(t, consumer)
	if string(delivery.RawData) != "not decoded here" || delivery.RawMessageID != "raw-1" {
		t.Fatalf("raw delivery = %+v", delivery)
	}
	if err := delivery.Ack(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRawDuplicateUsesNATSMessageID(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	first, err := client.PublishRaw(context.Background(), "moox.test.raw.v1", "same", []byte("body"), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.PublishRaw(context.Background(), "moox.test.raw.v1", "same", []byte("body"), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !second.Duplicate || first.Sequence != second.Sequence {
		t.Fatalf("acks = %+v %+v", first, second)
	}
}

func TestTLSURLsRequireHandshakeFirst(t *testing.T) {
	if !tlsHandshakeFirstRequired([]string{"tls://127.0.0.1:4222"}) {
		t.Fatal("TLS URL must use handshake-first")
	}
	if tlsHandshakeFirstRequired([]string{"nats://127.0.0.1:4222"}) {
		t.Fatal("plain NATS URL must not use handshake-first")
	}
}

func TestReconnectBufferFlushesPublishAfterServerRestart(t *testing.T) {
	port := reserveTestPort(t)
	storeDir := t.TempDir()
	start := func() *natsserver.Server {
		t.Helper()
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		go srv.Start()
		if !srv.ReadyForConnections(5 * time.Second) {
			srv.Shutdown()
			t.Fatal("nats server not ready")
		}
		return srv
	}

	srv := start()
	client, err := Connect(context.Background(), Config{
		URLs: []string{fmt.Sprintf("nats://127.0.0.1:%d", port)}, Name: "reconnect-buffer-test",
		ReconnectBufferBytes: 1024, ReconnectWait: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sub, err := client.nc.SubscribeSync("moox.test.reconnect")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	srv.Shutdown()
	srv.WaitForShutdown()
	waitForNATSStatus(t, client.nc, nats.RECONNECTING)
	if err := client.nc.Publish("moox.test.reconnect", []byte("buffered")); err != nil {
		t.Fatalf("publish while reconnecting: %v", err)
	}

	srv = start()
	defer srv.Shutdown()
	waitForNATSStatus(t, client.nc, nats.CONNECTED)
	msg, err := sub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("receive buffered publish after reconnect: %v", err)
	}
	if string(msg.Data) != "buffered" {
		t.Fatalf("message data = %q", msg.Data)
	}
}

func waitForNATSStatus(t *testing.T, connection *nats.Conn, want nats.Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if connection.Status() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nats status = %s, want %s", connection.Status(), want)
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

func ensureTestStream(t *testing.T, client *Client, name, subject string) {
	t.Helper()
	if _, err := client.js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{subject}, Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy}); err != nil {
		t.Fatalf("AddStream() error = %v", err)
	}
}

func newTestConsumer(t *testing.T, client *Client, durable string, ackWait time.Duration) *Consumer {
	t.Helper()
	consumer, err := client.NewConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: durable, FilterSubject: "moox.test.>", AckWait: ackWait, MaxDeliver: 5, MaxAckPending: 1000, FetchMaxWait: time.Second})
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}
	return consumer
}

func fetchOne(t *testing.T, consumer *Consumer) *Delivery {
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

func startTestServer(t *testing.T) (*natsserver.Server, string) {
	t.Helper()
	port := reserveTestPort(t)
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		t.Fatal("nats server not ready")
	}
	return srv, fmt.Sprintf("nats://127.0.0.1:%d", port)
}

func reserveTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
