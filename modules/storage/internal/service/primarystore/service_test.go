//go:build legacy_storage

package primarystore

import (
	"context"
	primary "github.com/mooyang-code/moox/modules/storage/internal/service/datashard"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primarystore/shardrouter"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDeleteTimeSeriesRowsUsesStrictCutoffKeys(t *testing.T) {
	primaryStore := &deletePrimary{}
	routes := &deleteMetadata{dataset: &pb.Dataset{SpaceId: "moox_system", DatasetId: "host_resource_v1", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}}}
	svc := &Service{
		metadataReader: routes,
		router:         routerForDelete(routes),
		primary:        primaryStore,
	}
	rsp, err := svc.DeleteTimeSeriesRows(context.Background(), &pb.DeleteTimeSeriesRowsReq{
		SpaceId: "moox_system", DatasetId: "host_resource_v1",
		TimeRange: &pb.TimeRange{EndTime: "2026-07-10T23:59:59.999999999Z"},
		Page:      &pb.Page{Size: 1000},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("DeleteTimeSeriesRows() err=%v rsp=%+v", err, rsp)
	}
	if len(primaryStore.deleted) != 1 || primaryStore.deleted[0].GetVersion() != "2026-07-10T23:59:00.000000000Z" {
		t.Fatalf("deleted keys = %+v, want only rows before cutoff", primaryStore.deleted)
	}
}

func TestDeleteTimeSeriesRowsRejectsLargeBatch(t *testing.T) {
	routes := &deleteMetadata{dataset: &pb.Dataset{SpaceId: "moox_system", DatasetId: "host_resource_v1", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}}}
	svc := &Service{metadataReader: routes}
	rsp, err := svc.DeleteTimeSeriesRows(context.Background(), &pb.DeleteTimeSeriesRowsReq{
		SpaceId: "moox_system", DatasetId: "host_resource_v1", TimeRange: &pb.TimeRange{EndTime: "2026-07-11T00:00:00Z"}, Page: &pb.Page{Size: 1001},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%+v err=%v, want invalid batch", rsp, err)
	}
}

func TestReadTimeSeriesRowsSubjectScanIncludesDimensions(t *testing.T) {
	routes := &deleteMetadata{dataset: &pb.Dataset{SpaceId: "moox_system", DatasetId: "host_fs_v1", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}}}
	svc := &Service{metadataReader: routes, router: routerForDelete(routes), primary: &dimensionPrimary{}}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "moox_system", DatasetId: "host_fs_v1", SubjectId: "agent-1", Freq: "1m"}},
		Page: &pb.Page{Size: 10},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ReadTimeSeriesRows() err=%v rsp=%+v", err, rsp)
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetDimensions()["device"] != "/dev/sda1" {
		t.Fatalf("rows=%+v, want dimension-bearing subject scan", rsp.GetRows())
	}
}

func routerForDelete(reader *deleteMetadata) *router.Resolver { return router.NewResolver(reader) }

type deleteMetadata struct {
	metadata.Reader
	dataset *pb.Dataset
}

func (m *deleteMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return m.dataset, nil
}
func (m *deleteMetadata) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return []*pb.PrimaryStoreRoute{{SpaceId: "moox_system", DatasetId: "host_resource_v1", RouteId: "route-1", SubjectPattern: "*", NodeId: "node-1", Status: "active"}}, &pb.PageResult{}, nil
}
func (*deleteMetadata) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return &pb.PrimaryStoreNode{NodeId: "node-1", Status: "active"}, nil
}
func (*deleteMetadata) ListDevices(context.Context, string, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return []*pb.Device{{DeviceId: "device-1", NodeId: "node-1", Engine: "pebble", Status: "active", Attributes: map[string]string{"shard_id": "storage-shard-0"}}}, &pb.PageResult{}, nil
}

type deletePrimary struct {
	deleted []*pb.ShardKey
}

func (*deletePrimary) WriteRows(context.Context, *pb.ShardTarget, []*pb.ShardRow) error {
	return nil
}
func (*deletePrimary) ReadRows(context.Context, *pb.ShardTarget, *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (p *deletePrimary) ScanRows(_ context.Context, _ *pb.ShardTarget, req *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	rows := []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "moox_system", DatasetId: "host_resource_v1", Key: "agent-1|1m|_", Version: "2026-07-10T23:59:00.000000000Z"}}}
	if req.GetVersionRange().GetEndVersion() >= "2026-07-11T00:00:00.000000000Z" {
		rows = append(rows, &pb.ShardRow{Key: &pb.ShardKey{SpaceId: "moox_system", DatasetId: "host_resource_v1", Key: "agent-1|1m|_", Version: "2026-07-11T00:00:00.000000000Z"}})
	}
	return rows, &pb.PageResult{}, nil
}
func (p *deletePrimary) DeleteRows(_ context.Context, _ *pb.ShardTarget, keys []*pb.ShardKey) error {
	p.deleted = append(p.deleted, keys...)
	return nil
}

var _ primary.Client = (*deletePrimary)(nil)
var _ primary.Deleter = (*deletePrimary)(nil)

type dimensionPrimary struct{}

func (*dimensionPrimary) WriteRows(context.Context, *pb.ShardTarget, []*pb.ShardRow) error {
	return nil
}
func (*dimensionPrimary) ReadRows(context.Context, *pb.ShardTarget, *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (*dimensionPrimary) ScanRows(context.Context, *pb.ShardTarget, *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	return []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "moox_system", DatasetId: "host_fs_v1", Key: "agent-1|1m|dimensions", Version: "2026-07-11T00:00:00.000000000Z"}, Attributes: map[string]string{"__moox_time_series_dimensions": `{"device":"/dev/sda1"}`}}}, &pb.PageResult{}, nil
}

var _ primary.Client = (*dimensionPrimary)(nil)

func TestServiceAccessorsReturnDependencies(t *testing.T) {
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	assert.NotNil(t, svc.MetadataStore())
	assert.NotNil(t, svc.MetadataReader())
}

func TestRefreshMetadataCacheNoOpsWithoutCache(t *testing.T) {
	svc := &Service{}
	assert.NoError(t, svc.refreshMetadataCache(context.Background()))
}

func TestRequireMetadataSchemaRejectsMissingTables(t *testing.T) {
	err := requireMetadataSchema(context.Background(), &stubMetadataStore{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing tables")
}

func TestLogViewErrorIgnoresNilError(t *testing.T) {
	logViewError(context.Background(), "stage", nil)
}

func TestOpenDefaultMetadataStoresInitializesSchema(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, cache, err := openDefaultMetadataStores(ctx, root, "", storageSchemaPath(t))
	require.NoError(t, err)
	require.NotNil(t, store)
	require.NotNil(t, cache)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		require.NoError(t, cache.Close())
	})

	space, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"})
	require.NoError(t, err)
	assert.Equal(t, "crypto", space.GetSpaceId())
}
