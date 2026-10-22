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

// 一个分区的慢投递不能占用另一个 durable 的拉取/ACK 预算。按生产形态验证：一条 Stream、
// 精确到数据集的过滤主题、互相独立的 durable。
func TestViewConsumerPartitionsKeepFactorIndependentFromSlowMisc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service, err := viewservice.New(filepath.Join(t.TempDir(), "view"), "partition-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := &storagegen.AuthInfo{AppId: "partition-e2e", AppKey: datanode.ServiceAuthKey("partition-secret", "partition-e2e")}
	service.SetPrimaryAuth(auth)
	service.SetPrimaryReader(concurrencyPrimaryReader{})
	prepareConcurrencyView(t, ctx, service, auth, "dataset_binance_kline_1m")
	prepareConcurrencyView(t, ctx, service, auth, "slow_dataset")

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
	slowBlocked := make(chan struct{})
	releaseSlow := make(chan struct{})
	var blockOnce sync.Once
	before := func(hookCtx context.Context, delivery *jetstream.Delivery) error {
		decoded := events.DecodeDelivery(registry, delivery)
		if decoded.Err != nil || decoded.Message.GetSubjectId() != "slow_dataset" {
			return nil
		}
		blockOnce.Do(func() { close(slowBlocked) })
		select {
		case <-releaseSlow:
			return nil
		case <-hookCtx.Done():
			return hookCtx.Err()
		}
	}
	stop, err := service.StartEventConsumer(ctx, client, viewservice.EventConsumerOptions{
		PartitionConfigs: []viewservice.EventConsumerOptions{
			{PartitionID: "factor", Consumer: events.StorageViewFactorConsumer, FilterSubjects: exactDatasetEventSubjects(t, registry, "quant", "dataset_binance_kline_1m"), FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, BeforeProcess: before},
			{PartitionID: "misc", Consumer: events.StorageViewMiscConsumer, FilterSubjects: exactDatasetEventSubjects(t, registry, "quant", "slow_dataset"), FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, BeforeProcess: before},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	publishPartitionRow(t, ctx, publisher, "quant", "slow_dataset", "slow-1")
	select {
	case <-slowBlocked:
	case <-time.After(3 * time.Second):
		t.Fatal("misc partition did not enter the blocked delivery")
	}
	publishPartitionRow(t, ctx, publisher, "quant", "dataset_binance_kline_1m", "kline-1")
	waitForConcurrencyRows(t, ctx, service, auth, "dataset_binance_kline_1m")
	state, err := client.ConsumerState(ctx, events.StorageViewConsumerStream, events.StorageViewFactorConsumer)
	if err != nil {
		t.Fatalf("read factor consumer state: %v", err)
	}
	if state.NumPending != 0 {
		t.Fatalf("slow Dataset leaked into the factor durable: pending=%d", state.NumPending)
	}
	close(releaseSlow)
}

func publishPartitionRow(t *testing.T, ctx context.Context, publisher *events.Publisher, spaceID, datasetID, recordID string) {
	t.Helper()
	payload := &storagepb.DatasetRowsUpserted{NodeId: "node", StoreId: "store", Sequence: 1, SpaceId: spaceID, DatasetId: datasetID, Rows: []*storagepb.RowUpsert{{
		Key:    &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: recordID, Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}},
		Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}}}
	if _, err := publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{EventID: spaceID + "-" + recordID, OccurredAt: time.Now().UTC(), SpaceID: spaceID, SubjectID: datasetID}); err != nil {
		t.Fatal(err)
	}
}
