//go:build cgo

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	storageoutbox "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// 按 cmd/server 的 primary 角色装配（SQLite 元数据、元数据缓存、Metadata 服务建数据集、带 DatasetExists 的 Pebble、
// PrimaryStore、后台连接 EventBus 的 Outbox 转发）再接一个 View：建数据集 → 写入 → 周期完成 → View 就绪，
// 账本判定完成（K 线）与写入方上报完成（因子）两条路都走一遍。View 侧用到的 View 列表与周期状态沿用压测的内存实现。
func TestPrimaryAndViewProcessesCompletePeriodFlow(t *testing.T) {
	subjects := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
	kline := readyBenchDataset{datasetID: "dataset_e2e_kline_5m", viewID: "view_e2e_kline_5m", columns: readyBenchKlineColumns, subjects: subjects}
	factor := readyBenchDataset{
		datasetID: "dataset_e2e_factor_5m", viewID: "view_e2e_factor_5m", subjects: subjects,
		columns: []readyBenchColumn{{"factor_000", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"factor_001", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	env := newPrimaryViewEnv(t, []readyBenchDataset{kline, factor})
	ctx := context.Background()
	params := readyBenchParams{rounds: 1, subjects: len(subjects), klineBatches: 1, factorCols: len(factor.columns)}
	if samples := runKlinePeriodBench(t, ctx, env, kline, params); len(samples) != 1 {
		t.Fatalf("kline samples=%d", len(samples))
	}
	if samples := runFactorPeriodBench(t, ctx, env, factor, params); len(samples) != 1 {
		t.Fatalf("factor samples=%d", len(samples))
	}
}

func newPrimaryViewEnv(t *testing.T, datasets []readyBenchDataset) readyBenchEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := &legacyReadyBenchEnv{auth: &pb.AuthInfo{AppId: "storage-primary"}}
	env.SetPeriodClock(readyBenchBase.Add(-time.Hour))

	// primary：元数据、缓存、Pebble、PrimaryStore、Metadata 服务。
	meta := openLifecycleStore(t, ctx)
	t.Cleanup(func() { _ = meta.Close() })
	if _, err := meta.UpsertSpace(ctx, &pb.Space{SpaceId: readyBenchSpace, Name: "压测空间"}); err != nil {
		t.Fatal(err)
	}
	cache, err := metacache.New(ctx, meta, metacache.Options{RefreshInterval: metacache.RefreshDisabled, RefreshTimeout: 5 * time.Second, InitialLoadTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	store, err := pebble.Open(pebble.Options{
		NodeID: "storage-node-0", Path: filepath.Join(t.TempDir(), "pebble"), MaxEventBytes: legacyBenchMaxBytes,
		PeriodNow: func() time.Time { return time.Unix(0, env.clock.Load()).UTC() },
		DatasetExists: func(spaceID, datasetID string) bool {
			_, err := cache.GetDataset(context.Background(), spaceID, datasetID)
			return err == nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env.primary, err = primarystore.New(primarystore.Options{Store: store, Validator: primarystore.NewMetadataValidator(cache), Snapshot: cache.RequestSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	metadataService, err := catalog.NewMetadataService(meta, cache, catalog.Options{Rows: store})
	if err != nil {
		t.Fatal(err)
	}
	for i, dataset := range datasets {
		columns := make([]*pb.DatasetColumn, len(dataset.columns))
		for j, column := range dataset.columns {
			columns[j] = &pb.DatasetColumn{ColumnName: column.name, OriginId: column.name, ValueType: column.kind, Status: "active", Attributes: map[string]string{"display_name": column.name}}
		}
		rsp, err := metadataService.CreateDataset(ctx, &pb.CreateDatasetReq{
			Dataset: &pb.Dataset{
				SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Name: fmt.Sprintf("压测数据%d", i),
				DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: readyBenchFreq,
				Attributes: map[string]string{"allowed_lateness_seconds": fmt.Sprint(readyBenchLatenessSeconds)},
			},
			Columns: columns,
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("建数据集 %s：rsp=%v err=%v", dataset.datasetID, rsp.GetRetInfo(), err)
		}
	}

	// EventBus：内嵌 JetStream，主题与生产的 MOOX_STORAGE 一致。
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), MaxPayload: legacyBenchMaxBytes, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("内嵌 NATS 没有启动")
	}
	t.Cleanup(server.Shutdown)
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "MOOX_STORAGE",
		Subjects: []string{
			"moox.event.storage.dataset.rows.upserted.v2.>", "moox.event.storage.dataset.period.completed.v1.>",
			"moox.event.storage.view.data.ready.v2.>", "moox.event.storage.dataset.sync_point.v1.>",
		},
		Storage: nats.FileStorage, Duplicates: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	env.watcher = newReadyWatcher(t, nc)
	config := jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-primary-e2e"}
	stopRelay, err := storageoutbox.StartWithEventBus(ctx, store, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopRelay)

	// view：建索引、挂接，再按数据集启动消费。
	viewEvents, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-view-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = viewEvents.Close() })
	view, err := viewservice.New(filepath.Join(t.TempDir(), "view"), legacyBenchSecret)
	if err != nil {
		t.Fatal(err)
	}
	env.view = view
	view.SetPrimaryAuth(env.auth)
	view.SetPrimaryReader(primaryFieldReader{service: env.primary})
	viewMetadata := newLegacyBenchMetadata()
	for _, dataset := range datasets {
		viewMetadata.views = append(viewMetadata.views, prepareLegacyBenchView(t, ctx, view, dataset))
	}
	if err := view.RestoreActiveViews(ctx, viewservice.MaintenanceOptions{Metadata: viewMetadata}); err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var filters []string
	for _, dataset := range datasets {
		filters = append(filters, exactDatasetEventSubjects(t, registry, readyBenchSpace, dataset.datasetID)...)
	}
	stopConsumer, err := view.StartEventConsumer(ctx, viewEvents, viewservice.EventConsumerOptions{
		PartitionID: "e2e", Consumer: "view_primary_e2e", FilterSubjects: filters,
		FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, AckWaitMS: 120000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopConsumer)
	return env
}
