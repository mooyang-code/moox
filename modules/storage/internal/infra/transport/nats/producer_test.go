package nats

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

func TestSubscribeDoesNotSerializeBlockedHandlers(t *testing.T) {
	srv, url := startTestNATSServer(t)
	defer srv.Shutdown()

	ctx := context.Background()
	producer, err := NewProducer(transport.ProducerOptions{
		ServerURL:      url,
		ConnectTimeout: time.Second,
		StreamName:     "MOOX_STORAGE_TEST",
		StreamSubjects: []string{"moox.storage.test.>"},
		ConsumerName:   "storage_view_test",
	})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer producer.Close()
	subscriber, ok := producer.(transport.Subscriber)
	if !ok {
		t.Fatal("producer does not implement Subscriber")
	}

	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	sub, err := subscriber.Subscribe(ctx, "moox.storage.test.rows_changed", func(_ context.Context, msg *transport.Message) error {
		body := string(msg.Data)
		started <- body
		if body == "first" {
			<-releaseFirst
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	defer close(releaseFirst)

	if err := producer.Send(ctx, &transport.Message{Subject: "moox.storage.test.rows_changed", Data: []byte("first")}); err != nil {
		t.Fatalf("Send first: %v", err)
	}
	if got := waitStarted(t, started); got != "first" {
		t.Fatalf("first handler started with %q", got)
	}

	if err := producer.Send(ctx, &transport.Message{Subject: "moox.storage.test.rows_changed", Data: []byte("second")}); err != nil {
		t.Fatalf("Send second: %v", err)
	}
	select {
	case got := <-started:
		if got != "second" {
			t.Fatalf("second handler started with %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second handler did not start while first handler was blocked")
	}
}

func waitStarted(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case got := <-started:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
		return ""
	}
}

func startTestNATSServer(t *testing.T) (*natsserver.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      port,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		t.Fatal("nats server not ready")
	}
	return srv, fmt.Sprintf("nats://127.0.0.1:%d", port)
}
