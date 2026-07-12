package cache

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubMetadataReader struct{}

func (stubMetadataReader) GetSpace(context.Context, string) (*pb.Space, error) {
	return nil, nil
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
func (stubMetadataReader) ListDataSources(context.Context, string, string, string, *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	return nil, nil
}
func (stubMetadataReader) ListSubjects(context.Context, string, string, string, []string, *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListSubjectSymbols(context.Context, string, string, string, string, *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return nil, nil
}
func (stubMetadataReader) ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) ListDatasetSubjects(context.Context, string, string, string, *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetField(context.Context, string, string) (*pb.Field, error) {
	return nil, nil
}
func (stubMetadataReader) ListFields(context.Context, string, pb.FieldValueType, *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
	return nil, nil, nil
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
func (stubMetadataReader) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return nil, nil
}
func (stubMetadataReader) ListPrimaryStoreNodes(context.Context, *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetDevice(context.Context, string) (*pb.Device, error) {
	return nil, nil
}
func (stubMetadataReader) ListDevices(context.Context, string, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return nil, nil, nil
}
func (stubMetadataReader) GetPrimaryStoreRoute(context.Context, string, string) (*pb.PrimaryStoreRoute, error) {
	return nil, nil
}
func (stubMetadataReader) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
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
