package metadata

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/stretchr/testify/require"
)

func TestInitSchema_ShouldCreateMetadataTables(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "metadata", "storage_metadata.db")

	err := InitSchema(ctx, SchemaOptions{
		Storage: storageconfig.StorageConfig{
			Root: tmp,
			Metadata: storageconfig.StorageMetadata{
				Path: dbPath,
			},
		},
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql"))
}
