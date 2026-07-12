package metadata

import (
	"context"
	"errors"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

func TestImportSeed_FullSampleSeed_ShouldImportEntities(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	seedPath := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "config", "metadata.seed.yaml"))

	result, err := ImportSeed(ctx, SeedOptions{
		Storage: storageconfig.StorageConfig{
			Root: tmp,
			Metadata: storageconfig.StorageMetadata{
				Path: filepath.Join(tmp, "metadata", "storage_metadata.db"),
			},
		},
		SchemaPath: schemaPath(t),
		SeedPath:   seedPath,
	})
	require.NoError(t, err)
	assert.Greater(t, result.DataSources, 0)
	assert.Greater(t, result.Datasets, 0)
	assert.Greater(t, result.Subjects, 0)
}

func TestParseValueTypeMappings(t *testing.T) {
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_STRING, parseValueType("string"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, parseValueType("double"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, parseValueType("unknown"))
}

func TestParseDataKindMappings(t *testing.T) {
	assert.Equal(t, pb.DataKind_DATA_KIND_TIME_SERIES, parseDataKind("time_series"))
	assert.Equal(t, pb.DataKind_DATA_KIND_RECORD, parseDataKind("record"))
	assert.Equal(t, pb.DataKind_DATA_KIND_UNSPECIFIED, parseDataKind("unknown"))
}

func TestParseColumnOriginMappings(t *testing.T) {
	assert.Equal(t, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, parseDatasetColumnOriginType("field"))
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION, parseColumnOriginType("expression"))
}

func TestSeedErrFormatsKindAndID(t *testing.T) {
	err := seedErr("space", "crypto", errors.New("boom"))
	assert.Contains(t, err.Error(), "space")
	assert.Contains(t, err.Error(), "crypto")
}
