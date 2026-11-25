package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

type fakeSeedLister struct {
	keys []store.SeedKey
	err  error
}

func (f *fakeSeedLister) RunningSeeds(context.Context) ([]store.SeedKey, error) { return f.keys, f.err }

type fakeSeedSink struct{ keys []store.SeedKey }

func (f *fakeSeedSink) SetHistorySeeds(keys []store.SeedKey) { f.keys = keys }

func openSeedStore(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 取到的补齐集合落库并交给 hub；取不到时沿用上一次；重启后先沿用库里的，从没取到过按没有补齐。
func TestHistorySeedSyncerPersistsAndFallsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factor.db")
	db := openSeedStore(t, path)
	lister := &fakeSeedLister{keys: []store.SeedKey{{SpaceID: "stockus", DatasetID: "kline", Freq: "5m"}}}
	sink := &fakeSeedSink{}
	syncer := &historySeedSyncer{lister: lister, db: db, sink: sink}

	syncer.load(ctx)
	require.Empty(t, sink.keys, "从没取到过按没有补齐")

	require.NoError(t, syncer.refresh(ctx))
	require.Equal(t, lister.keys, sink.keys)

	lister.err = errors.New("Collector 不可达")
	require.Error(t, syncer.refresh(ctx))
	require.Equal(t, lister.keys, sink.keys, "调不通沿用上一次")

	// 管理端重启：新进程启动时先沿用库里的结果。
	require.NoError(t, db.Close())
	reopened := openSeedStore(t, path)
	restarted := &fakeSeedSink{}
	(&historySeedSyncer{lister: lister, db: reopened, sink: restarted}).load(ctx)
	require.Equal(t, []store.SeedKey{{SpaceID: "stockus", DatasetID: "kline", Freq: "5m"}}, restarted.keys)

	// 补齐结束：取到空集合，库与 hub 随之清空。
	lister.err, lister.keys = nil, nil
	restartedSyncer := &historySeedSyncer{lister: lister, db: reopened, sink: restarted}
	require.NoError(t, restartedSyncer.refresh(ctx))
	require.Empty(t, restarted.keys)
	saved, err := reopened.ListHistorySeeds(ctx)
	require.NoError(t, err)
	require.Empty(t, saved)
}

type fakeInvoker struct {
	service, method string
	req             *collectorpb.ListHistorySeedsReq
	rsp             *collectorpb.ListHistorySeedsRsp
}

func (f *fakeInvoker) Invoke(_ context.Context, service, method string, req, rsp any) error {
	f.service, f.method, f.req = service, method, req.(*collectorpb.ListHistorySeedsReq)
	proto.Merge(rsp.(*collectorpb.ListHistorySeedsRsp), f.rsp)
	return nil
}

// 经网关只取补齐中的记录，转成（空间、数据集、频率）；返回码非成功时报错。
func TestGatewayCollectorListsRunningSeeds(t *testing.T) {
	invoker := &fakeInvoker{rsp: &collectorpb.ListHistorySeedsRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Seeds: []*collectorpb.HistorySeed{
			{SpaceId: "stockus", DatasetId: "kline", Frequency: "5m", Status: "running"},
			{SpaceId: "stockcn", DatasetId: "kline", Frequency: "5m", Status: "done"},
		},
	}}
	keys, err := gatewayCollector{gateway: invoker}.RunningSeeds(context.Background())
	require.NoError(t, err)
	require.Equal(t, "trpc.moox.collector.CollectMgr", invoker.service)
	require.Equal(t, "ListHistorySeeds", invoker.method)
	require.True(t, invoker.req.GetOnlyRunning())
	require.Equal(t, []store.SeedKey{{SpaceID: "stockus", DatasetID: "kline", Freq: "5m"}}, keys)

	invoker.rsp = &collectorpb.ListHistorySeedsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "boom"}}
	_, err = gatewayCollector{gateway: invoker}.RunningSeeds(context.Background())
	require.ErrorContains(t, err, "boom")
}
