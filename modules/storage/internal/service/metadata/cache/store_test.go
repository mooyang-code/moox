package cache

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubMetadataReader struct{}

func (stubMetadataReader) GetSpace(context.Context, string) (*pb.Space, error) {
	return nil, nil
}

func TestCompositeIDDoesNotCollideOnDots(t *testing.T) {
	if compositeID("a.b", "c") == compositeID("a", "b.c") {
		t.Fatal("composite metadata IDs collide")
	}
}
func (stubMetadataReader) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return []*pb.Space{{
		SpaceId: "crypto",
		Name:    "Crypto",
		Owner:   "tester",
		Status:  "active",
	}}, nil, nil
}
func (stubMetadataReader) GetView(context.Context, string, string) (*pb.View, error) {
	return nil, nil
}
func (stubMetadataReader) ListViews(context.Context, string, string, string, *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	return nil, nil
}
func (stubMetadataReader) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDataSource(context.Context, string, string) (*pb.DataSource, error) {
	return nil, nil
}
func (stubMetadataReader) ListDataSources(context.Context, string, string, string, string, *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	return nil, nil
}
func (stubMetadataReader) ListSubjects(context.Context, string, string, string, []string, string, *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListSubjectSymbols(context.Context, string, string, string, string, *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return nil, nil
}
func (stubMetadataReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListDatasetSubjects(context.Context, string, string, string, *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetFieldGroup(context.Context, string, string) (*pb.FieldGroup, error) {
	return nil, nil
}
func (stubMetadataReader) ListFieldGroups(context.Context, string, string, *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetField(context.Context, string, string) (*pb.Field, error) {
	return nil, nil
}
func (stubMetadataReader) ListFields(context.Context, metadata.FieldQuery) ([]*pb.Field, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) CountFieldsByGroup(context.Context, string) (metadata.FieldGroupCounts, error) {
	return metadata.FieldGroupCounts{ByGroup: map[string]uint64{}}, nil
}
func (stubMetadataReader) GetFactor(context.Context, string, string) (*pb.Factor, error) {
	return nil, nil
}
func (stubMetadataReader) ListFactors(context.Context, string, string, *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDataNode(context.Context, string) (*pb.DataNode, error) {
	return nil, nil
}
func (stubMetadataReader) ListDataNodes(context.Context, *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDevice(context.Context, string) (*pb.Device, error) {
	return nil, nil
}
func (stubMetadataReader) ListDevices(context.Context, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListArchiveFiles(context.Context, string, string, *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	return nil, nil, nil
}

var _ metadata.Reader = stubMetadataReader{}

func TestNew_NilBase_ShouldReturnError(t *testing.T) {
	_, err := New(context.Background(), nil, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata base store is required")
}

func TestStore_GetSpace_AfterStart_ShouldReturnCachedSpace(t *testing.T) {
	ctx := context.Background()
	store, err := New(ctx, stubMetadataReader{}, Options{
		RefreshInterval:    RefreshDisabled,
		InitialLoadTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	space, err := store.GetSpace(ctx, "crypto")
	require.NoError(t, err)
	require.NotNil(t, space)
	assert.Equal(t, "crypto", space.GetSpaceId())
	assert.Equal(t, "Crypto", space.GetName())
}

func TestStore_ListSpaces_ShouldFilterByOwner(t *testing.T) {
	ctx := context.Background()
	store, err := New(ctx, stubMetadataReader{}, Options{
		RefreshInterval:    RefreshDisabled,
		InitialLoadTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	spaces, page, err := store.ListSpaces(ctx, "tester", nil)
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	assert.Equal(t, "crypto", spaces[0].GetSpaceId())
	require.NotNil(t, page)
	assert.False(t, page.GetHasMore())
}

type dataNodeMetadataReader struct {
	stubMetadataReader
	nodeCalls    int
	datasetCalls int
}

func (r *dataNodeMetadataReader) ListDataNodes(context.Context, *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	r.nodeCalls++
	return []*pb.DataNode{{NodeId: "node-a", Name: "Node A", Status: "active"}}, &pb.PageResult{Page: 1, Size: 1000, Total: 1}, nil
}

func (r *dataNodeMetadataReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	r.datasetCalls++
	return []*pb.Dataset{{
		SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Dataset", Status: "active", BindingLocked: true, Revision: 7,
	}}, &pb.PageResult{Page: 1, Size: 1000, Total: 1}, nil
}

func TestStore_CachePreservesDataNodeAndDatasetLifecycleFields(t *testing.T) {
	reader := &dataNodeMetadataReader{}
	store, err := New(context.Background(), reader, Options{
		RefreshInterval:    RefreshDisabled,
		InitialLoadTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	node, err := store.GetDataNode(context.Background(), "node-a")
	require.NoError(t, err)
	assert.Equal(t, "node-a", node.GetNodeId())
	dataset, err := store.GetDataset(context.Background(), "space", "dataset")
	require.NoError(t, err)
	assert.True(t, dataset.GetBindingLocked())
	assert.Equal(t, uint64(7), dataset.GetRevision())
	assert.Equal(t, 1, reader.nodeCalls)
	assert.Equal(t, 1, reader.datasetCalls)

	snapshot := store.RequestSnapshot()
	got, ok := snapshot.GetDataset("space", "dataset")
	assert.True(t, ok)
	assert.True(t, got.GetBindingLocked())
	assert.Equal(t, uint64(7), got.GetRevision())
	gotNode, ok := snapshot.GetDataNode("node-a")
	assert.True(t, ok)
	assert.Equal(t, "node-a", gotNode.GetNodeId())
}
