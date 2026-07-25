package test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/consumer"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestArchiveConsumesUpdatesAndMaterializesMonthlyParquet(t *testing.T) {
	storageSubject := "moox.storage.dataset.rows.upserted.v1.>"
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer ns.Shutdown()
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{storageSubject}, Storage: nats.FileStorage})
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{Name: "archive-e2e", Durable: "archive-e2e", FilterSubject: storageSubject, AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: -1})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store, err := journal.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := writer.New(store, root, 100)
	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv([]string{ns.ClientURL()}, "archive-e2e"))
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
	pull, err := events.NewConsumer(context.Background(), client, registry, events.ConsumerConfig{
		Name: "archive-e2e", Event: events.DatasetRowsUpserted, AckWait: time.Minute,
		MaxDeliver: 5, MaxAckPending: 256, FetchMaxWait: 100 * time.Millisecond,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pull.Close()
	h := consumer.NewHandler(consumer.NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}}), store, nil)
	runner := consumer.NewRunner(pull, h, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()

	publish := func(id, dataTime, close string) {
		event := &sharedpb.DatasetRowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*sharedpb.RowUpsert{{Key: &sharedpb.RowKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", Kind: &sharedpb.RowKey_TimeSeries{TimeSeries: &sharedpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: dataTime}}}, Fields: []*sharedpb.FieldValue{{FieldId: "close", Value: &sharedpb.TypedValue{Value: &sharedpb.TypedValue_DoubleValue{DoubleValue: parseFloat(t, close)}}}}}}}
		_, err := publisher.Publish(context.Background(), events.DatasetRowsUpserted, event, events.PublishOptions{EventID: id, OccurredAt: time.Now().UTC(), SpaceID: event.GetSpaceId(), SubjectID: event.GetDatasetId()})
		if err != nil {
			t.Fatal(err)
		}
	}
	publish("e1", "2026-06-30T23:59:00Z", "100")
	publish("e2", "2026-06-30T23:59:00Z", "101")
	publish("e3", "2026-07-01T00:00:00Z", "102")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.WriteDirty(context.Background(), 100); err == nil {
			june := domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}
			july := domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202607"}
			jp, _ := june.AbsolutePath(root)
			yp, _ := july.AbsolutePath(root)
			jr, _, _, je := parquetio.Read(jp)
			yr, _, _, ye := parquetio.Read(yp)
			if je == nil && ye == nil && len(jr) == 1 && len(yr) == 1 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("archive files were not materialized")
}

func parseFloat(t *testing.T, value string) float64 {
	var out float64
	if _, err := fmt.Sscan(value, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
