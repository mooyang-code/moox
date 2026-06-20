package cache_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreServesRouteMetadataFromSnapshotAfterBaseClosed(t *testing.T) {
	ctx := context.Background()
	base := openTestSQLiteStore(t)

	_, err := base.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = base.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = base.UpsertDataSet(ctx, &pb.DataSet{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance", Name: "Kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
	require.NoError(t, err)
	_, err = base.UpsertStorageNode(ctx, &pb.StorageNode{NodeId: "node-1", Name: "node-1", Endpoint: "127.0.0.1:19001", Status: "active"})
	require.NoError(t, err)
	_, err = base.UpsertDevice(ctx, &pb.Device{DeviceId: "pebble-1", NodeId: "node-1", Name: "pebble-1", Engine: "pebble", Status: "active"})
	require.NoError(t, err)
	_, err = base.UpsertStorageRoute(ctx, &pb.StorageRoute{SpaceId: "crypto", RouteId: "route-1", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-1", Status: "active"})
	require.NoError(t, err)

	store, err := metacache.New(ctx, base, metacache.Options{RefreshInterval: metacache.RefreshDisabled})
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, base.Close())

	target, err := router.NewResolver(store).Resolve(ctx, &pb.DataScope{
		SpaceId:   "crypto",
		DatasetId: "kline",
		SubjectId: "APT-USDT",
	})
	require.NoError(t, err)
	require.Equal(t, "node-1", target.GetNodeId())
	require.Equal(t, "pebble-1", target.GetDeviceId())
	require.Equal(t, "crypto/kline", target.GetDeviceTable())
}

func TestStoreRefreshesSnapshotAfterWrites(t *testing.T) {
	ctx := context.Background()
	base := openTestSQLiteStore(t)
	defer base.Close()

	_, err := base.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)

	store, err := metacache.New(ctx, base, metacache.Options{RefreshInterval: metacache.RefreshDisabled})
	require.NoError(t, err)
	defer store.Close()

	space, err := store.GetSpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "crypto", space.GetName())

	_, err = base.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "changed-outside-cache"})
	require.NoError(t, err)
	space, err = store.GetSpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "crypto", space.GetName())

	require.NoError(t, store.Refresh(ctx))
	space, err = store.GetSpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "changed-outside-cache", space.GetName())
}

func TestStoreLoadsMultipleRelationshipRowsWithCompositeIDs(t *testing.T) {
	ctx := context.Background()
	base := openTestSQLiteStore(t)
	defer base.Close()

	_, err := base.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = base.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = base.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "okx", Name: "OKX", Kind: "exchange"})
	require.NoError(t, err)
	_, err = base.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", Name: "APT-USDT", SubjectType: "crypto_pair"})
	require.NoError(t, err)
	_, err = base.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "AR-USDT", Name: "AR-USDT", SubjectType: "crypto_pair"})
	require.NoError(t, err)
	_, err = base.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{SpaceId: "crypto", SubjectId: "APT-USDT", DataSourceId: "binance", ExternalSymbol: "APTUSDT"})
	require.NoError(t, err)
	_, err = base.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{SpaceId: "crypto", SubjectId: "AR-USDT", DataSourceId: "okx", ExternalSymbol: "AR-USDT"})
	require.NoError(t, err)
	_, err = base.UpsertDataSet(ctx, &pb.DataSet{SpaceId: "crypto", DatasetId: "symbols", DataSourceId: "binance", Name: "symbols", DataKind: pb.DataKind_DATA_KIND_TABLE})
	require.NoError(t, err)
	_, err = base.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	_, err = base.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "AR-USDT"})
	require.NoError(t, err)

	store, err := metacache.New(ctx, base, metacache.Options{RefreshInterval: metacache.RefreshDisabled})
	require.NoError(t, err)
	defer store.Close()

	symbols, page, err := store.ListSubjectSymbols(ctx, "crypto", "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, symbols, 2)

	subjects, err := store.ListDataSetSubjects(ctx, "crypto", "symbols")
	require.NoError(t, err)
	require.Len(t, subjects, 2)
}

func openTestSQLiteStore(t *testing.T) *metasqlite.Store {
	t.Helper()
	ctx := context.Background()
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(t.TempDir(), "storage_metadata.db"),
		SchemaPath: storageMetadataSchemaPath(t),
	})
	require.NoError(t, err)
	require.NoError(t, store.InitSchema(ctx))
	return store
}

func storageMetadataSchemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../schema/storage_metadata.sql"))
}
