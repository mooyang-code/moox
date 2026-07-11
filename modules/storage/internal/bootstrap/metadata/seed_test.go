package metadata

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
)

func TestImportSeedPreservesAttributes(t *testing.T) {
	seedPath := filepath.Join(t.TempDir(), "seed.yaml")
	seed := []byte(`spaces:
  - space_id: stock_cn
    name: 中国股票
    owner: collector
    status: active
    attributes:
      scope: builtin
      owner_module: collector
`)
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "metadata.db")
	result, err := ImportSeed(context.Background(), SeedOptions{Storage: storageconfig.StorageConfig{Metadata: storageconfig.StorageMetadata{Path: storePath}}, SchemaPath: metadataSchemaPath(t), SeedPath: seedPath})
	if err != nil {
		t.Fatalf("ImportSeed: %v", err)
	}
	if result.Spaces != 1 {
		t.Fatalf("spaces = %d, want 1", result.Spaces)
	}
	store, err := openStoreForTest(t, storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	space, err := store.GetSpace(context.Background(), "stock_cn")
	if err != nil {
		t.Fatal(err)
	}
	if space.GetAttributes()["scope"] != "builtin" || space.GetAttributes()["owner_module"] != "collector" {
		t.Fatalf("attributes = %#v", space.GetAttributes())
	}
}

func TestImportSeedRejectsUnknownFields(t *testing.T) {
	seedPath := filepath.Join(t.TempDir(), "seed.yaml")
	if err := os.WriteFile(seedPath, []byte("spaces:\n  - space_id: stock_cn\n    name: CN\n    typo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ImportSeed(context.Background(), SeedOptions{Storage: storageconfig.StorageConfig{Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}}, SeedPath: seedPath})
	if err == nil {
		t.Fatal("unknown seed field was accepted")
	}
}

func metadataSchemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql"))
}

func openStoreForTest(t *testing.T, path string) (*metasqlite.Store, error) {
	t.Helper()
	return metasqlite.Open(context.Background(), metasqlite.Options{Path: path, SchemaPath: metadataSchemaPath(t)})
}
