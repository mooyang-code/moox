package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
