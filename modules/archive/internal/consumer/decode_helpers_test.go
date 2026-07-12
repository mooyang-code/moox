package consumer

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTime(t *testing.T) {
	ts, err := parseTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	assert.Equal(t, time.UTC, ts.Location())
	_, err = parseTime("")
	require.Error(t, err)
}

func TestMergePatch(t *testing.T) {
	a := domain.RowPatch{
		Attributes: map[string]string{"source": "live"},
		Columns:    map[string]domain.Scalar{"open": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	b := domain.RowPatch{
		Attributes: map[string]string{"batch": "2"},
		Columns:    map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		WrittenAt:  time.Unix(2, 0).UTC(),
	}
	merged := mergePatch(a, b)
	assert.Equal(t, "live", merged.Attributes["source"])
	assert.Equal(t, "2", merged.Attributes["batch"])
	assert.Contains(t, merged.Columns, "close")
	assert.Equal(t, b.WrittenAt, merged.WrittenAt)
}
