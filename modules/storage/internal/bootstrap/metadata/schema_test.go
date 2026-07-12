package metadata

import (
	"context"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"runtime"
	"testing"
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

func TestParseDataKindCoversAllKnownKinds(t *testing.T) {
	assert.Equal(t, pb.DataKind_DATA_KIND_SNAPSHOT, parseDataKind("snapshot"))
	assert.Equal(t, pb.DataKind_DATA_KIND_EVENT, parseDataKind("event"))
	assert.Equal(t, pb.DataKind_DATA_KIND_DOCUMENT, parseDataKind("document"))
	assert.Equal(t, pb.DataKind_DATA_KIND_TABLE, parseDataKind("table"))
}

func TestParseValueTypeCoversRemainingTypes(t *testing.T) {
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_INT, parseValueType("int"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_BOOL, parseValueType("bool"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_TIME, parseValueType("time"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_JSON, parseValueType("json"))
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_BYTES, parseValueType("bytes"))
}

func TestParseDatasetColumnOriginMappings(t *testing.T) {
	assert.Equal(t, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR, parseDatasetColumnOriginType("factor"))
	assert.Equal(t, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, parseDatasetColumnOriginType("system"))
}

func TestParseColumnOriginMappingsExtra(t *testing.T) {
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, parseColumnOriginType("dataset_column"))
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM, parseColumnOriginType("system"))
}
