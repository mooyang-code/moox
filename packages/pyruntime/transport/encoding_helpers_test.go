package transport

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
