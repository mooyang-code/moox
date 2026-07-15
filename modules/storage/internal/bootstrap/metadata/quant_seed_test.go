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
	if result.Spaces != 7 || result.FieldGroups != 25 || result.Fields != 96 || result.Datasets != 31 || result.DatasetColumns != 122 {
		t.Fatalf("unexpected import result: %+v", result)
	}
}
