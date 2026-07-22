package jetstream

import (
	"context"
	"testing"
	"time"
)

func TestPublishRawAndFetchRaw(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	client := connectTestClient(t, url)
	defer client.Close()
	ensureTestStream(t, client, "TEST", "moox.test.>")
	consumer, err := client.NewPullConsumer(context.Background(), ConsumerConfig{Stream: "TEST", Durable: "raw", FilterSubject: "moox.test.>", AckWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	if _, err := client.PublishRaw(context.Background(), "moox.test.raw.v1", "raw-1", []byte("protobuf-body"), "application/vnd.moox.event+protobuf"); err != nil {
		t.Fatal(err)
	}
	deliveries, err := consumer.FetchRaw(context.Background(), 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("FetchRaw() deliveries=%v err=%v", deliveries, err)
	}
	if string(deliveries[0].RawData) != "protobuf-body" || deliveries[0].RawMessageID != "raw-1" {
		t.Fatalf("raw delivery = %+v", deliveries[0])
	}
	if err := deliveries[0].Ack(context.Background()); err != nil {
		t.Fatal(err)
	}
}
