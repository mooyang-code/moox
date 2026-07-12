package parquetio

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsWrittenPartition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partition.parquet")
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	closeValue := 1.25
	rows := []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		DimensionsJSON: "{}", WrittenAt: time.Now().UTC(),
		Columns: map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}},
	}}
	_, err := Write(path, rows, WriteOptions{Generation: 3, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024})
	require.NoError(t, err)
	manifest, err := Validate(path, key, 3)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), manifest.RowCount)
}

func TestValidateRejectsGenerationMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partition.parquet")
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	closeValue := 1.0
	rows := []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		DimensionsJSON: "{}", WrittenAt: time.Now().UTC(),
		Columns: map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}},
	}}
	_, err := Write(path, rows, WriteOptions{Generation: 1, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024})
	require.NoError(t, err)
	_, err = Validate(path, key, 2)
	require.Error(t, err)
}

func TestAnyToScalarAndAsString(t *testing.T) {
	scalar, err := anyToScalar(storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, 1.5)
	require.NoError(t, err)
	require.NotNil(t, scalar.Double)
	assert.InDelta(t, 1.5, *scalar.Double, 0.001)
	assert.Equal(t, "abc", asString("abc"))
}
