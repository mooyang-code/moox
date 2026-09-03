//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	storagegen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// A slow metrics delivery must not consume the Kline durable's pull/ACK
// budget. This exercises the production shape: one Stream, exact Dataset
// filters, and independent durables.
func TestViewConsumerPartitionsKeepKlineIndependentFromMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service, err := viewservice.New(filepath.Join(t.TempDir(), "view"), "partition-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := &storagegen.AuthInfo{AppId: "partition-e2e", AppKey: datanode.ServiceAuthKey("partition-secret", "partition-e2e")}
	service.SetPrimaryAuth(auth)
	service.SetPrimaryReader(concurrencyPrimaryReader{})
	prepareConcurrencyView(t, ctx, service, auth, "dataset_binance_spot_kline_1m")
	prepareConcurrencyView(t, ctx, service, auth, "dataset_mooxsys_service_metrics")
	prepareConcurrencyView(t, ctx, service, auth, "other_dataset")

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
	if _, err := js.AddStream(&nats.StreamConfig{Name: events.StorageViewConsumerStream, Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "storage-view-partition-e2e"))
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
	metricsBlocked := make(chan struct{})
	releaseMetrics := make(chan struct{})
	var blockOnce sync.Once
	before := func(hookCtx context.Context, delivery *jetstream.Delivery) error {
		decoded := events.DecodeDelivery(registry, delivery)
		if decoded.Err != nil || decoded.Message.GetSubjectId() != "dataset_mooxsys_service_metrics" {
			return nil
		}
		blockOnce.Do(func() { close(metricsBlocked) })
		select {
		case <-releaseMetrics:
			return nil
		case <-hookCtx.Done():
			return hookCtx.Err()
		}
	}
	stop, err := service.StartEventConsumer(ctx, client, viewservice.EventConsumerOptions{
		PartitionConfigs: []viewservice.EventConsumerOptions{
			{PartitionID: "kline", Consumer: events.StorageViewKlineConsumer, FilterSubjects: exactDatasetEventSubjects(t, registry, "quant", "dataset_binance_spot_kline_1m"), FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, BeforeProcess: before},
			{PartitionID: "system_metrics", Consumer: events.StorageViewMetricsConsumer, FilterSubjects: exactDatasetEventSubjects(t, registry, "quant", "dataset_mooxsys_service_metrics"), FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, BeforeProcess: before},
			{PartitionID: "misc", Consumer: events.StorageViewMiscConsumer, FilterSubjects: exactDatasetEventSubjects(t, registry, "quant", "other_dataset"), FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, BeforeProcess: before},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	publishPartitionRow(t, ctx, publisher, "quant", "dataset_mooxsys_service_metrics", "metrics-1")
	select {
	case <-metricsBlocked:
	case <-time.After(3 * time.Second):
		t.Fatal("metrics partition did not enter the blocked delivery")
	}
	publishPartitionRow(t, ctx, publisher, "quant", "dataset_binance_spot_kline_1m", "kline-1")
	waitForConcurrencyRows(t, ctx, service, auth, "dataset_binance_spot_kline_1m")
	publishPartitionRow(t, ctx, publisher, "quant", "other_dataset", "other-1")
	waitForConcurrencyRows(t, ctx, service, auth, "other_dataset")
	for _, durable := range []string{events.StorageViewKlineConsumer, events.StorageViewMiscConsumer} {
		state, stateErr := client.ConsumerState(ctx, events.StorageViewConsumerStream, durable)
		if stateErr != nil {
			t.Fatalf("read %s consumer state: %v", durable, stateErr)
		}
		if durable == events.StorageViewKlineConsumer && state.NumPending != 0 {
			t.Fatalf("other Dataset leaked into Kline durable: pending=%d", state.NumPending)
		}
	}
	close(releaseMetrics)
}

func publishPartitionRow(t *testing.T, ctx context.Context, publisher *events.Publisher, spaceID, datasetID, recordID string) {
	t.Helper()
	payload := &storagepb.DatasetRowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: []*storagepb.RowUpsert{{
		Key:    &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: recordID, Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}},
		Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}}}
	if _, err := publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{EventID: spaceID + "-" + recordID, OccurredAt: time.Now().UTC(), SpaceID: spaceID, SubjectID: datasetID}); err != nil {
		t.Fatal(err)
	}
}
