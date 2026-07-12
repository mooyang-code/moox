package transport

import (
	"reflect"
	"testing"
	"time"
	"encoding/json"
	"math"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONTableRoundTrip(t *testing.T) {
	want := Table{Columns: []string{"close"}, Rows: [][]any{{1.2}, {nil}}}
	b, e := EncodeJSON(want)
	if e != nil {
		t.Fatal(e)
	}
	got, e := DecodeJSON(b)
	if e != nil || len(got.Rows) != 2 {
		t.Fatalf("got=%+v err=%v", got, e)
	}
}

func TestArrowStreamRoundTripPreservesTypesAndNulls(t *testing.T) {
	want := Table{
		Columns: []string{"price", "count", "enabled", "symbol", "when", "empty"},
		Rows: [][]any{
			{1.25, int64(2), true, "BTC", time.UnixMilli(1710000000123).UTC(), nil},
			{nil, int64(3), false, "ETH", time.UnixMilli(1710000001123).UTC(), nil},
		},
	}
	b, err := EncodeArrowStream(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeArrowStream(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 16 || len(got.Rows) != len(want.Rows) || !reflect.DeepEqual(got.Columns, want.Columns) {
		t.Fatalf("got columns/rows=%+v bytes=%d", got, len(b))
	}
	// Arrow timestamps are represented as UTC milliseconds at the transport
	// boundary, which is stable across Go and Python implementations.
	if got.Rows[0][0] != want.Rows[0][0] || got.Rows[0][1] != int64(2) || got.Rows[0][4] != want.Rows[0][4].(time.Time).UnixMilli() {
		t.Fatalf("unexpected decoded values: %#v", got.Rows)
	}
	if got.Rows[1][0] != nil || got.Rows[0][5] != nil {
		t.Fatalf("null values lost: %#v", got.Rows)
	}
}

func TestArrowFileRoundTrip(t *testing.T) {
	want := Table{Columns: []string{"value"}, Rows: [][]any{{int64(1)}, {int64(2)}}}
	b, err := EncodeArrowFile(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeArrowFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestArrowRejectsRaggedRows(t *testing.T) {
	if _, err := EncodeArrowStream(Table{Columns: []string{"a", "b"}, Rows: [][]any{{1}}}); err == nil {
		t.Fatal("expected ragged row error")
	}
}

func TestKindNameCoversAllKinds(t *testing.T) {
	assert.Equal(t, "int64", kindName(kindInt64))
	assert.Equal(t, "float64", kindName(kindFloat64))
	assert.Equal(t, "bool", kindName(kindBool))
	assert.Equal(t, "string", kindName(kindString))
	assert.Equal(t, "timestamp", kindName(kindTimestamp))
	assert.Equal(t, "null", kindName(columnKind(99)))
}

func TestToInt64AcceptsNumericTypes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{name: "int", in: int(7), want: 7, ok: true},
		{name: "int32", in: int32(8), want: 8, ok: true},
		{name: "int64", in: int64(9), want: 9, ok: true},
		{name: "uint8", in: uint8(10), want: 10, ok: true},
		{name: "json number int", in: json.Number("11"), want: 11, ok: true},
		{name: "uint64 overflow", in: uint64(math.MaxUint64), want: 0, ok: false},
		{name: "string", in: "bad", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toInt64(tt.in)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestToFloat64AcceptsNumericTypes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{name: "int", in: int(3), want: 3, ok: true},
		{name: "float32", in: float32(1.5), want: 1.5, ok: true},
		{name: "float64", in: 2.25, want: 2.25, ok: true},
		{name: "json number float", in: json.Number("3.5"), want: 3.5, ok: true},
		{name: "string", in: "bad", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat64(tt.in)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.InDelta(t, tt.want, got, 0.0001)
			}
		})
	}
}

func TestInferKindPromotesIntToFloat(t *testing.T) {
	rows := [][]any{{int64(1)}, {2.5}}
	kind, err := inferKind(rows, 0)
	require.NoError(t, err)
	assert.Equal(t, kindFloat64, kind)
}

func TestInferKindRejectsMixedIncompatibleTypes(t *testing.T) {
	rows := [][]any{{int64(1)}, {"text"}}
	_, err := inferKind(rows, 0)
	require.Error(t, err)
}

func TestValueKindOfRecognizesTimestamp(t *testing.T) {
	assert.Equal(t, kindTimestamp, valueKindOf(time.UnixMilli(1).UTC()))
}

func TestBuildColumnRejectsNonFiniteFloat(t *testing.T) {
	rows := [][]any{{math.NaN()}}
	_, err := buildColumn(rows, 0, kindFloat64)
	require.Error(t, err)
}

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
