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
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	eventstoragepb "github.com/mooyang-code/moox/packages/events/storagepb"
	"github.com/mooyang-code/moox/packages/jetstream"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

func TestArchiveConsumesUpdatesAndMaterializesMonthlyParquet(t *testing.T) {
	storageSubject := "moox.storage.rows.upserted.v1.>"
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
	pull, err := client.BindManagedPullConsumer(context.Background(), jetstream.ConsumerBindRef{Stream: "MOOX_STORAGE", Durable: "archive-e2e", FetchMaxWait: 100 * time.Millisecond, DeliverDecodeErrors: true})
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
		event := &storagepb.RowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: dataTime}}}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: parseFloat(t, close)}}}}}}}
		payload, err := proto.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		_, err = publisher.Publish(context.Background(), events.StorageRowsUpserted, &eventstoragepb.RowsUpserted{DatasetId: event.GetDatasetId(), Rows: payload}, events.PublishOptions{EventID: id, OccurredAt: time.Now().UTC(), SpaceID: event.GetSpaceId(), SubjectID: event.GetDatasetId()})
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
