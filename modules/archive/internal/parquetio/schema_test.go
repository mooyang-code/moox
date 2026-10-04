package parquetio

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsWrittenPartition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partition.parquet")
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", SeriesTag: "venue:okx", Month: "202601"}
	closeValue := 1.25
	rows := []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		WrittenAt: time.Now().UTC(),
		Columns:   map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}},
	}}
	_, err := Write(path, rows, WriteOptions{Generation: 3, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024})
	require.NoError(t, err)
	manifest, err := Validate(path, key, 3)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), manifest.RowCount)
}

func TestValidatePreservesAllNullColumnsAndMaterializedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partition.parquet")
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", SeriesTag: "", Month: "202601"}
	materializedAt := time.Date(2026, 1, 3, 4, 5, 6, 7, time.UTC)
	rows := []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		WrittenAt: time.Now().UTC(), Columns: map[string]domain.Scalar{},
	}}
	_, err := Write(path, rows, WriteOptions{
		Generation: 3, MaterializedAt: materializedAt, RowGroupRows: 1024,
		Columns: map[string]storagepb.FieldValueType{"all_null": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	})
	require.NoError(t, err)
	manifest, err := Validate(path, key, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"all_null"}, manifest.Columns)
	require.Equal(t, materializedAt, manifest.MaterializedAt)
}

func TestValidateRejectsGenerationMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partition.parquet")
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	closeValue := 1.0
	rows := []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		WrittenAt: time.Now().UTC(),
		Columns:   map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}},
	}}
	_, err := Write(path, rows, WriteOptions{Generation: 1, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024})
	require.NoError(t, err)
	_, err = Validate(path, key, 2)
	require.Error(t, err)
}

func TestReadRejectsOptionalOrNullSeriesTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "optional-tag.parquet")
	schema := parquet.NewSchema("invalid_v2", parquet.Group{
		colCandleTime: parquet.Timestamp(parquet.Nanosecond),
		colSpace:      parquet.String(),
		colDataset:    parquet.String(),
		colSubject:    parquet.String(),
		colFreq:       parquet.String(),
		colSeriesTag:  parquet.Optional(parquet.String()),
		colAttributes: parquet.String(),
		colWrittenAt:  parquet.Timestamp(parquet.Nanosecond),
	})
	file, err := os.Create(path)
	require.NoError(t, err)
	writer := parquet.NewGenericWriter[map[string]any](file, schema)
	writer.SetKeyValueMetadata("moox.archive.schema_version", "2")
	_, err = writer.Write([]map[string]any{{
		colCandleTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		colSpace:      "crypto", colDataset: "kline", colSubject: "BTC", colFreq: "1h",
		colSeriesTag: nil, colAttributes: "{}", colWrittenAt: time.Now().UTC(),
	}})
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
	_, _, _, err = Read(path)
	require.ErrorContains(t, err, "system columns")
}

func TestBuildSchemaMakesSeriesTagRequired(t *testing.T) {
	schema, err := BuildSchema(nil)
	require.NoError(t, err)
	require.True(t, hasRequiredSchemaField(schema, colSeriesTag))
}

func TestBuildSchemaRejectsLegacyDimensionsColumn(t *testing.T) {
	_, err := BuildSchema(map[string]storagepb.FieldValueType{"dimensions_json": storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON})
	require.Error(t, err)
}

func TestAnyToScalarAndAsString(t *testing.T) {
	scalar, err := anyToScalar(storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, 1.5)
	require.NoError(t, err)
	require.NotNil(t, scalar.Double)
	assert.InDelta(t, 1.5, *scalar.Double, 0.001)
	assert.Equal(t, "abc", asString("abc"))
}
