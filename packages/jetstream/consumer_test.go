package jetstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func testConsumerConfig(name string) ConsumerConfig {
	return ConsumerConfig{
		Stream: "TEST", Durable: name, FilterSubject: "moox.test.>",
		AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: 100 * time.Millisecond, DeliverPolicy: nats.DeliverAllPolicy,
	}
}

func TestNewConsumerCreatesWhenMissing(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer, err := client.NewConsumer(context.Background(), testConsumerConfig("created"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	if _, err := client.js.ConsumerInfo("TEST", "created"); err != nil {
		t.Fatal(err)
	}
}

func TestNewConsumerBindsMatchingConsumer(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := testConsumerConfig("matching")
	first, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestNewConsumerUpdatesMutableFields(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := testConsumerConfig("mutable")
	first, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	cfg.AckWait = 2 * time.Second
	cfg.MaxDeliver = 5
	cfg.MaxAckPending = 16
	second, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	info, _ := client.js.ConsumerInfo("TEST", "mutable")
	if info.Config.AckWait != cfg.AckWait || info.Config.MaxDeliver != 5 || info.Config.MaxAckPending != 16 {
		t.Fatalf("updated config = %+v", info.Config)
	}
}

func TestNewConsumerRejectsImmutableConflict(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := testConsumerConfig("conflict")
	first, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	cfg.FilterSubject = "moox.test.other.>"
	if _, err := client.NewConsumer(context.Background(), cfg); !errors.Is(err, ErrConsumerConfigConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestNewConsumerDoesNotDeleteConflictingConsumer(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := testConsumerConfig("preserved")
	first, err := client.NewConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	conflict := cfg
	conflict.DeliverPolicy = nats.DeliverNewPolicy
	if _, err := client.NewConsumer(context.Background(), conflict); !errors.Is(err, ErrConsumerConfigConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	info, err := client.js.ConsumerInfo("TEST", "preserved")
	if err != nil || info.Config.DeliverPolicy != nats.DeliverAllPolicy {
		t.Fatalf("consumer was replaced: info=%v err=%v", info, err)
	}
}

func TestNewConsumerRequiresExplicitSettings(t *testing.T) {
	if _, err := (&Client{}).NewConsumer(context.Background(), ConsumerConfig{}); !errors.Is(err, ErrInvalidConsumer) {
		t.Fatalf("error = %v, want invalid consumer", err)
	}
}

func TestDeliveryOperationsRequireContext(t *testing.T) {
	var d *Delivery
	for name, fn := range map[string]func(context.Context) error{
		"ack": d.Ack, "nak": func(ctx context.Context) error { return d.Nak(ctx, time.Second) },
		"in progress": d.InProgress, "term": d.Term,
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(context.Background()); !errors.Is(err, ErrInvalidDelivery) {
				t.Fatalf("error = %v, want ErrInvalidDelivery", err)
			}
		})
	}
}
