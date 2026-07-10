package nats

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestStreamConfigFromOptionsAppliesRetentionLimits(t *testing.T) {
	cfg := streamConfigFromOptions(transport.ProducerOptions{
		StreamName:     "MOOX_STORAGE_TEST",
		StreamSubjects: []string{"moox.storage.test.>"},
		MaxAge:         24 * time.Hour,
		MaxMsgs:        500000,
		MaxBytes:       256 * 1024 * 1024,
	})

	if cfg.Name != "MOOX_STORAGE_TEST" || cfg.Storage != nats.FileStorage {
		t.Fatalf("stream config = %+v", cfg)
	}
	if cfg.MaxAge != 24*time.Hour {
		t.Fatalf("MaxAge = %s, want 24h", cfg.MaxAge)
	}
	if cfg.MaxMsgs != 500000 {
		t.Fatalf("MaxMsgs = %d, want 500000", cfg.MaxMsgs)
	}
	if cfg.MaxBytes != 256*1024*1024 {
		t.Fatalf("MaxBytes = %d, want 256MiB", cfg.MaxBytes)
	}
	if cfg.Discard != nats.DiscardOld {
		t.Fatalf("Discard = %v, want DiscardOld", cfg.Discard)
	}
}

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

func TestSubscribeHonorsMaxInFlight(t *testing.T) {
	srv, url := startTestNATSServer(t)
	defer srv.Shutdown()

	ctx := context.Background()
	producer, err := NewProducer(transport.ProducerOptions{
		ServerURL:      url,
		ConnectTimeout: time.Second,
		StreamName:     "MOOX_STORAGE_TEST_LIMIT",
		StreamSubjects: []string{"moox.storage.limit.>"},
		ConsumerName:   "storage_view_limit_test",
		MaxInFlight:    1,
		AckWait:        2 * time.Second,
		MaxDeliver:     3,
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
	sub, err := subscriber.Subscribe(ctx, "moox.storage.limit.rows_changed", func(_ context.Context, msg *transport.Message) error {
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

	if err := producer.Send(ctx, &transport.Message{Subject: "moox.storage.limit.rows_changed", Data: []byte("first")}); err != nil {
		t.Fatalf("Send first: %v", err)
	}
	if got := waitStarted(t, started); got != "first" {
		t.Fatalf("first handler started with %q", got)
	}
	if err := producer.Send(ctx, &transport.Message{Subject: "moox.storage.limit.rows_changed", Data: []byte("second")}); err != nil {
		t.Fatalf("Send second: %v", err)
	}

	select {
	case got := <-started:
		t.Fatalf("handler %q started while max_in_flight=1 first handler was blocked", got)
	case <-time.After(200 * time.Millisecond):
	}

	releaseFirst <- struct{}{}
	if got := waitStarted(t, started); got != "second" {
		t.Fatalf("second handler started with %q", got)
	}
}

func TestSubscribeNaksFailedHandlerForRedelivery(t *testing.T) {
	srv, url := startTestNATSServer(t)
	defer srv.Shutdown()

	ctx := context.Background()
	producer, err := NewProducer(transport.ProducerOptions{
		ServerURL:      url,
		ConnectTimeout: time.Second,
		StreamName:     "MOOX_STORAGE_TEST_RETRY",
		StreamSubjects: []string{"moox.storage.retry.>"},
		ConsumerName:   "storage_view_retry_test",
		AckWait:        2 * time.Second,
		MaxDeliver:     3,
	})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer producer.Close()
	subscriber := producer.(transport.Subscriber)

	attempts := make(chan int, 2)
	attempt := 0
	sub, err := subscriber.Subscribe(ctx, "moox.storage.retry.rows_changed", func(context.Context, *transport.Message) error {
		attempt++
		attempts <- attempt
		if attempt == 1 {
			return errors.New("view index owner unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if err := producer.Send(ctx, &transport.Message{Subject: "moox.storage.retry.rows_changed", Data: []byte("event")}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := waitAttempt(t, attempts); got != 1 {
		t.Fatalf("first attempt = %d", got)
	}
	if got := waitAttempt(t, attempts); got != 2 {
		t.Fatalf("redelivery attempt = %d", got)
	}
}

func waitAttempt(t *testing.T, attempts <-chan int) int {
	t.Helper()
	select {
	case attempt := <-attempts:
		return attempt
	case <-time.After(3 * time.Second):
		t.Fatal("message was not redelivered")
		return 0
	}
}

func TestSubscribeUpdatesExistingDurableConsumerConfig(t *testing.T) {
	srv, url := startTestNATSServer(t)
	defer srv.Shutdown()

	ctx := context.Background()
	streamName := "MOOX_STORAGE_TEST_DRIFT"
	subject := "moox.storage.drift.rows_changed"
	consumerName := durableConsumerName("storage_view_drift_test", subject)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{"moox.storage.drift.>"},
		Storage:  nats.FileStorage,
	}); err != nil {
		t.Fatalf("AddStream: %v", err)
	}
	if _, err := js.AddConsumer(streamName, &nats.ConsumerConfig{
		Durable:        consumerName,
		DeliverPolicy:  nats.DeliverNewPolicy,
		AckPolicy:      nats.AckExplicitPolicy,
		AckWait:        30 * time.Second,
		MaxDeliver:     3,
		MaxAckPending:  1000,
		ReplayPolicy:   nats.ReplayInstantPolicy,
		FilterSubject:  subject,
		DeliverSubject: nc.NewInbox(),
	}); err != nil {
		t.Fatalf("AddConsumer: %v", err)
	}

	producer, err := NewProducer(transport.ProducerOptions{
		ServerURL:      url,
		ConnectTimeout: time.Second,
		StreamName:     streamName,
		StreamSubjects: []string{"moox.storage.drift.>"},
		ConsumerName:   "storage_view_drift_test",
		MaxInFlight:    32,
		AckWait:        2 * time.Minute,
		MaxDeliver:     10,
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
	sub, err := subscriber.Subscribe(ctx, subject, func(_ context.Context, _ *transport.Message) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	info, err := js.ConsumerInfo(streamName, consumerName)
	if err != nil {
		t.Fatalf("ConsumerInfo: %v", err)
	}
	if info.Config.AckWait != 2*time.Minute {
		t.Fatalf("AckWait = %s, want 2m", info.Config.AckWait)
	}
	if info.Config.MaxAckPending != 32 {
		t.Fatalf("MaxAckPending = %d, want 32", info.Config.MaxAckPending)
	}
	if info.Config.MaxDeliver != 10 {
		t.Fatalf("MaxDeliver = %d, want 10", info.Config.MaxDeliver)
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
