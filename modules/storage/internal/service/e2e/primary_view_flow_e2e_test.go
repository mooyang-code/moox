//go:build cgo

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 按 cmd/server 的 primary 角色装配（SQLite 元数据、元数据缓存、Metadata 服务建数据集、带 DatasetExists 的 Pebble、
// PrimaryStore、后台连接 EventBus 的 Outbox 转发）再接一个跟随变更日志的 View：View 的维护流程从 Primary 建索引，
// 之后建数据集 → 写入 → 周期完成 → View 就绪，账本判定完成（K 线）与写入方上报完成（因子）两条路都走一遍。
func TestPrimaryAndViewProcessesCompletePeriodFlow(t *testing.T) {
	subjects := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
	kline := readyBenchDataset{datasetID: "dataset_e2e_kline_5m", viewID: "view_e2e_kline_5m", columns: readyBenchKlineColumns, subjects: subjects}
	factor := readyBenchDataset{
		datasetID: "dataset_e2e_factor_5m", viewID: "view_e2e_factor_5m", subjects: subjects,
		columns: []readyBenchColumn{{"factor_000", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"factor_001", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	env := newPrimaryViewEnv(t, []readyBenchDataset{kline, factor})
	ctx := context.Background()
	env.WaitBuilt(t, ctx, kline.viewID)
	env.WaitBuilt(t, ctx, factor.viewID)
	params := readyBenchParams{rounds: 1, subjects: len(subjects), klineBatches: 1, factorCols: len(factor.columns)}
	if samples := runKlinePeriodBench(t, ctx, env, kline, params); len(samples) != 1 {
		t.Fatalf("kline samples=%d", len(samples))
	}
	if samples := runFactorPeriodBench(t, ctx, env, factor, params); len(samples) != 1 {
		t.Fatalf("factor samples=%d", len(samples))
	}
	state := env.WaitBuilt(t, ctx, kline.viewID)
	if state.GetAppliedCursor().GetSequence() == 0 || state.GetViewRevision() != 1 || len(state.GetColumns()) != len(kline.columns) {
		t.Fatalf("GetViewState=%v", state)
	}
}

func newPrimaryViewEnv(t *testing.T, datasets []readyBenchDataset) *followEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
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
	return newFollowEnv(t, datasets, followEnvOptions{
		snapshot:  cache.RequestSnapshot,
		validator: primarystore.NewMetadataValidator(cache),
		datasetExists: func(spaceID, datasetID string) bool {
			_, err := cache.GetDataset(context.Background(), spaceID, datasetID)
			return err == nil
		},
		setup: func(store *pebble.Store) {
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
						AllowedLatenessSeconds: uint32(readyBenchLatenessSeconds),
					},
					Columns: columns,
				})
				if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
					t.Fatalf("建数据集 %s：rsp=%v err=%v", dataset.datasetID, rsp.GetRetInfo(), err)
				}
			}
		},
	})
}
