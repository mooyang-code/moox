package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestRegistryReconcilesStorageFanoutTopologyAndDeduplicatesPublish(t *testing.T) {
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer server.Shutdown()

	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(js, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile managed topology: %v", err)
	}
	if _, err := reg.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile managed topology: %v", err)
	}
	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv([]string{server.ClientURL()}, "eventbus-topology-e2e"))
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
	consumers := make(map[string]*events.Consumer, 3)
	for _, durable := range []string{events.StorageViewKlineConsumer, "factor_calc", "moox_archive_kline_v2"} {
		consumer, err := events.NewConsumer(context.Background(), client, registry, events.ConsumerConfig{
			Name: durable, Event: events.DatasetRowsUpserted, AckWait: time.Minute,
			MaxDeliver: 5, MaxAckPending: 256, FetchMaxWait: 100 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("bind %s: %v", durable, err)
		}
		consumers[durable] = consumer
		defer consumer.Close()
	}

	occurredAt := time.Now().UTC()
	payload := &storagepb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "dataset_binance_spot_kline_1m", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "dataset_binance_spot_kline_1m", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}}}
	first, err := publisher.Publish(context.Background(), events.DatasetRowsUpserted, payload, events.PublishOptions{EventID: "storage-e2e-1", OccurredAt: occurredAt, SpaceID: "crypto", SubjectID: "dataset_binance_spot_kline_1m"})
	if err != nil || first == nil || first.Duplicate {
		t.Fatalf("first publish ack=%+v err=%v", first, err)
	}
	second, err := publisher.Publish(context.Background(), events.DatasetRowsUpserted, payload, events.PublishOptions{EventID: "storage-e2e-1", OccurredAt: occurredAt, SpaceID: "crypto", SubjectID: "dataset_binance_spot_kline_1m"})
	if err != nil || second == nil || !second.Duplicate {
		t.Fatalf("duplicate publish ack=%+v err=%v", second, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for durable, consumer := range consumers {
		deliveries, err := consumer.Fetch(ctx, 1)
		if err != nil && len(deliveries) == 0 {
			t.Fatalf("fetch %s: %v", durable, err)
		}
		if len(deliveries) != 1 {
			t.Fatalf("deliveries %s=%+v", durable, deliveries)
		}
		delivery := deliveries[0]
		message, _, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
		if err != nil {
			t.Fatalf("decode %s: %v", durable, err)
		}
		if message.GetEventId() != "storage-e2e-1" || message.GetEventName() != events.DatasetRowsUpserted.Name() || message.GetSpaceId() != "crypto" || message.GetSubjectId() != "dataset_binance_spot_kline_1m" {
			t.Fatalf("decoded envelope %s=%+v", durable, message)
		}
		if err := delivery.Ack(ctx); err != nil {
			t.Fatalf("ack %s: %v", durable, err)
		}
	}
}
