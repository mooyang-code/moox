package jetstream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestNewPullConsumerRequiresDurableAndStream(t *testing.T) {
	client := &Client{}
	_, err := client.NewPullConsumer(context.Background(), ConsumerConfig{})
	if !errors.Is(err, ErrInvalidConsumer) {
		t.Fatalf("NewPullConsumer() error = %v, want ErrInvalidConsumer", err)
	}
}

func TestNewPullConsumerHonorsCanceledContext(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := client.NewPullConsumer(ctx, ConsumerConfig{Stream: "TEST", Durable: "cancelled", FilterSubject: "moox.test.>"})
	if !errors.Is(err, ErrConnection) || !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("NewPullConsumer() error = %v, elapsed = %s", err, time.Since(started))
	}
}

func TestPullConsumerSequentialRestartPreservesDurableConfiguration(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := ConsumerConfig{Stream: "TEST", Durable: "restart-worker", FilterSubject: "moox.test.>", AckWait: 250 * time.Millisecond, MaxDeliver: 4, MaxAckPending: 17}
	first, err := client.NewPullConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first NewPullConsumer() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	second, err := client.NewPullConsumer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second NewPullConsumer() error = %v", err)
	}
	defer second.Close()
	info, err := client.js.ConsumerInfo("TEST", "restart-worker")
	if err != nil {
		t.Fatalf("ConsumerInfo() error = %v", err)
	}
	if info.Config.FilterSubject != cfg.FilterSubject || info.Config.AckWait != cfg.AckWait || info.Config.MaxDeliver != cfg.MaxDeliver || info.Config.MaxAckPending != cfg.MaxAckPending {
		t.Fatalf("durable config = %+v, want filter=%q ack_wait=%s max_deliver=%d max_ack_pending=%d", info.Config, cfg.FilterSubject, cfg.AckWait, cfg.MaxDeliver, cfg.MaxAckPending)
	}
}

func TestPullConsumerConcurrentCreationIsIdempotent(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	cfg := ConsumerConfig{Stream: "TEST", Durable: "race-worker", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumer, err := client.NewPullConsumer(context.Background(), cfg)
			if err == nil {
				err = consumer.Close()
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent NewPullConsumer() error = %v", err)
		}
	}
}

func TestPullConsumerRejectsDurableConfigurationDrift(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	base := ConsumerConfig{Stream: "TEST", Durable: "drift-worker", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8}
	consumer, err := client.NewPullConsumer(context.Background(), base)
	if err != nil {
		t.Fatalf("base NewPullConsumer() error = %v", err)
	}
	defer consumer.Close()
	for name, changed := range map[string]ConsumerConfig{
		"filter":      func() ConsumerConfig { c := base; c.FilterSubject = "moox.test.events.>"; return c }(),
		"ack wait":    func() ConsumerConfig { c := base; c.AckWait = 2 * time.Second; return c }(),
		"max deliver": func() ConsumerConfig { c := base; c.MaxDeliver = 4; return c }(),
		"ack pending": func() ConsumerConfig { c := base; c.MaxAckPending = 9; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.NewPullConsumer(context.Background(), changed); !errors.Is(err, ErrInvalidConsumer) {
				t.Fatalf("NewPullConsumer() error = %v, want ErrInvalidConsumer", err)
			}
		})
	}
}

func TestNewPullConsumerRequiresFilterSubject(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	_, err := client.NewPullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: "missing-filter"})
	if !errors.Is(err, ErrInvalidConsumer) {
		t.Fatalf("NewPullConsumer() error = %v, want ErrInvalidConsumer", err)
	}
}

func TestBindPullConsumerNeverCreates(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	_, err := client.BindPullConsumer(context.Background(), ConsumerRef{Stream: "TEST", Durable: "missing", FilterSubject: "moox.test.>"})
	if !errors.Is(err, ErrConsumerNotFound) {
		t.Fatalf("BindPullConsumer() error=%v, want not found", err)
	}
	if _, err := client.js.ConsumerInfo("TEST", "missing"); !errors.Is(err, nats.ErrConsumerNotFound) {
		t.Fatalf("missing consumer was created: %v", err)
	}
	created, err := client.EnsurePullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: "existing", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8})
	if err != nil {
		t.Fatal(err)
	}
	_ = created.Close()
	bound, err := client.BindPullConsumer(context.Background(), ConsumerRef{Stream: "TEST", Durable: "existing", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8})
	if err != nil {
		t.Fatal(err)
	}
	_ = bound.Close()
}

func TestBindManagedPullConsumerUsesServerConfiguration(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	serverCfg := &nats.ConsumerConfig{
		Name:          "managed-worker",
		Durable:       "managed-worker",
		FilterSubject: "moox.test.server.>",
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       250 * time.Millisecond,
		MaxDeliver:    7,
		MaxAckPending: 3,
		DeliverPolicy: nats.DeliverNewPolicy,
	}
	if _, err := client.js.AddConsumer("TEST", serverCfg); err != nil {
		t.Fatalf("AddConsumer() error = %v", err)
	}

	bound, err := client.BindManagedPullConsumer(context.Background(), ConsumerBindRef{
		Stream: "TEST", Durable: "managed-worker", FetchMaxWait: 37 * time.Millisecond, DeliverDecodeErrors: true,
	})
	if err != nil {
		t.Fatalf("BindManagedPullConsumer() error = %v", err)
	}
	defer bound.Close()

	if bound.cfg.FilterSubject != serverCfg.FilterSubject || bound.cfg.AckWait != serverCfg.AckWait ||
		bound.cfg.MaxDeliver != serverCfg.MaxDeliver || bound.cfg.MaxAckPending != serverCfg.MaxAckPending ||
		bound.cfg.DeliverPolicy != serverCfg.DeliverPolicy {
		t.Fatalf("bound config = %+v, want server config %+v", bound.cfg, serverCfg)
	}
	if bound.cfg.FetchMaxWait != 37*time.Millisecond || !bound.cfg.DeliverDecodeErrors {
		t.Fatalf("client overrides were not applied: %+v", bound.cfg)
	}
	after, err := client.js.ConsumerInfo("TEST", "managed-worker")
	if err != nil {
		t.Fatalf("ConsumerInfo() after bind error = %v", err)
	}
	if after.Config.FilterSubject != serverCfg.FilterSubject || after.Config.AckWait != serverCfg.AckWait ||
		after.Config.MaxDeliver != serverCfg.MaxDeliver || after.Config.MaxAckPending != serverCfg.MaxAckPending ||
		after.Config.DeliverPolicy != serverCfg.DeliverPolicy {
		t.Fatalf("server config changed after managed bind: %+v", after.Config)
	}
}

func TestBindManagedPullConsumerNeverCreates(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")

	_, err := client.BindManagedPullConsumer(context.Background(), ConsumerBindRef{Stream: "TEST", Durable: "missing-managed"})
	if !errors.Is(err, ErrConsumerNotFound) {
		t.Fatalf("BindManagedPullConsumer() error = %v, want ErrConsumerNotFound", err)
	}
	if _, err := client.js.ConsumerInfo("TEST", "missing-managed"); !errors.Is(err, nats.ErrConsumerNotFound) {
		t.Fatalf("missing consumer was created: %v", err)
	}
}

func TestDeliveryOperationsRequireContext(t *testing.T) {
	var d *Delivery
	for name, fn := range map[string]func(context.Context) error{
		"ack":         d.Ack,
		"nak":         func(ctx context.Context) error { return d.Nak(ctx, time.Second) },
		"in progress": d.InProgress,
		"term":        d.Term,
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(context.Background()); !errors.Is(err, ErrInvalidDelivery) {
				t.Fatalf("error = %v, want ErrInvalidDelivery", err)
			}
		})
	}
}
