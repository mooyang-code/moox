package metadata

import (
	"context"
	"path/filepath"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
)

func TestImportQuantInitialSeedIntoEmptyStore(t *testing.T) {
	result, err := ImportSeed(context.Background(), SeedOptions{
		Storage:    storageconfig.StorageConfig{Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}},
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
		SeedPath:   filepath.Join("..", "..", "..", "..", "..", "examples", "metadata-quant-initial.seed.yaml"),
	})
	if err != nil {
		t.Fatalf("ImportSeed: %v", err)
	}
	if result.Spaces != 4 || result.FieldGroups != 13 || result.Fields != 45 || result.Datasets != 10 || result.DatasetColumns != 73 || result.Views != 10 || result.ViewColumns != 63 {
		t.Fatalf("unexpected import result: %+v", result)
	}
}
