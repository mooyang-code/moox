package jetstream

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go"
	"testing"
	"time"
)

func TestDeliveryTokenRejectsTampering(t *testing.T) {
	if _, err := decodeDeliveryToken("not-base64"); err == nil {
		t.Fatal("malformed token accepted")
	}
	if _, err := encodeDeliveryToken("TEST", "worker", "$JS.ACK.OTHER.worker.1.1"); err == nil {
		t.Fatal("mismatched ack subject accepted")
	}
}

func TestPersistentTokenAckAfterClientRecreation(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer, err := client.EnsurePullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: "token-worker", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Publish(context.Background(), validTestMessage("token-msg", "moox.test.events.v1")); err != nil {
		t.Fatal(err)
	}
	ds, err := consumer.Fetch(context.Background(), 1)
	if err != nil || len(ds) != 1 {
		t.Fatalf("fetch=%v err=%v", ds, err)
	}
	token := ds[0].PersistentToken
	if token == "" {
		t.Fatal("persistent token is empty")
	}
	_ = consumer.Close()
	_ = client.Close()
	client = connectTestClient(t, url)
	defer client.Close()
	if err := client.AckToken(context.Background(), token); err != nil {
		t.Fatalf("AckToken() error=%v", err)
	}
	bound, err := client.BindPullConsumer(context.Background(), ConsumerRef{Stream: "TEST", Durable: "token-worker", FilterSubject: "moox.test.>", AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = bound.Fetch(ctx, 1)
	if !errors.Is(err, ErrConnection) && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("message redelivered after token ACK: %v", err)
	}
}
