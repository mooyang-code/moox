package transport

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeJSONRejectsInvalid(t *testing.T) {
	_, err := DecodeJSON([]byte(`{bad`))
	require.Error(t, err)
}

func TestEncodeDecodeJSONRoundTrip(t *testing.T) {
	in := Table{Columns: []string{"a", "b"}, Rows: [][]any{{int64(1), "x"}, {int64(2), "y"}}}
	raw, err := EncodeJSON(in)
	require.NoError(t, err)
	out, err := DecodeJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, in.Columns, out.Columns)
	require.Len(t, out.Rows, 2)
}

func TestEncodeDecodeArrowStreamEmptyAndTyped(t *testing.T) {
	empty, err := EncodeArrowStream(Table{})
	require.NoError(t, err)
	decoded, err := DecodeArrowStream(empty)
	require.NoError(t, err)
	assert.Empty(t, decoded.Columns)

	table := Table{
		Columns: []string{"i", "f", "b", "s", "t", "n"},
		Rows: [][]any{
			{int64(1), 1.5, true, "hi", time.UnixMilli(1000).UTC(), nil},
			{int64(2), 2.5, false, "lo", time.UnixMilli(2000).UTC(), nil},
		},
	}
	raw, err := EncodeArrowStream(table)
	require.NoError(t, err)
	got, err := DecodeArrowStream(raw)
	require.NoError(t, err)
	assert.Equal(t, table.Columns, got.Columns)
	require.Len(t, got.Rows, 2)
}

func TestEncodeDecodeArrowFile(t *testing.T) {
	table := Table{Columns: []string{"v"}, Rows: [][]any{{int64(9)}}}
	raw, err := EncodeArrowFile(table)
	require.NoError(t, err)
	got, err := DecodeArrowFile(raw)
	require.NoError(t, err)
	assert.Equal(t, table.Columns, got.Columns)
	require.Len(t, got.Rows, 1)
}

func TestTableRecordRejectsMismatchedRow(t *testing.T) {
	_, _, _, err := tableRecord(Table{Columns: []string{"a"}, Rows: [][]any{{1, 2}}})
	require.Error(t, err)
}

func TestBuildColumnCoversKinds(t *testing.T) {
	rows := [][]any{{true}, {false}, {nil}}
	col, err := buildColumn(rows, 0, kindBool)
	require.NoError(t, err)
	require.NotNil(t, col)
	col.Release()

	rows = [][]any{{"a"}, {nil}, {"b"}}
	col, err = buildColumn(rows, 0, kindString)
	require.NoError(t, err)
	col.Release()

	ts := time.UnixMilli(1).UTC()
	rows = [][]any{{ts}, {nil}}
	col, err = buildColumn(rows, 0, kindTimestamp)
	require.NoError(t, err)
	col.Release()

	rows = [][]any{{nil}, {nil}}
	col, err = buildColumn(rows, 0, kindNull)
	require.NoError(t, err)
	col.Release()

	rows = [][]any{{int64(1)}, {nil}}
	col, err = buildColumn(rows, 0, kindInt64)
	require.NoError(t, err)
	col.Release()

	_, err = buildColumn([][]any{{math.Inf(1)}}, 0, kindFloat64)
	require.Error(t, err)
}

func TestKindTypeMapping(t *testing.T) {
	assert.NotNil(t, kindType(kindInt64))
	assert.NotNil(t, kindType(kindFloat64))
	assert.NotNil(t, kindType(kindBool))
	assert.NotNil(t, kindType(kindString))
	assert.NotNil(t, kindType(kindTimestamp))
	assert.NotNil(t, kindType(kindNull))
}
