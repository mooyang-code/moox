package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportSeed_EmptySeedPath_ShouldReturnError(t *testing.T) {
	_, err := ImportSeed(context.Background(), SeedOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata seed path is required")
}

func TestImportSeed_MinimalSeed_ShouldImportSpace(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	seedPath := filepath.Join(tmp, "seed.yaml")
	require.NoError(t, os.WriteFile(seedPath, []byte(`spaces:
  - space_id: test-space
    name: Test Space
    status: active
`), 0o644))

	dbPath := filepath.Join(tmp, "metadata", "storage_metadata.db")
	result, err := ImportSeed(ctx, SeedOptions{
		Storage: storageconfig.StorageConfig{
			Root: tmp,
			Metadata: storageconfig.StorageMetadata{
				Path: dbPath,
			},
		},
		SchemaPath: schemaPath(t),
		SeedPath:   seedPath,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Spaces)
}
