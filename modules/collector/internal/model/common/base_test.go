package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDecimal_StringAndFloat(t *testing.T) {
	d := NewDecimal("1.25")
	assert.Equal(t, "1.25", d.String())
	f, err := d.Float64()
	require.NoError(t, err)
	assert.InDelta(t, 1.25, f, 1e-9)
}

func TestDecimal_MarshalUnmarshalJSON(t *testing.T) {
	d := NewDecimal("3.14")
	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var got Decimal
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "3.14", got.String())

	var fromNumber Decimal
	require.NoError(t, json.Unmarshal([]byte("2.5"), &fromNumber))
	assert.Equal(t, "2.50000000", fromNumber.String())
}

func TestNewBaseDataPoint_ShouldPopulateFields(t *testing.T) {
	dp := NewBaseDataPoint("binance", "kline")
	assert.Equal(t, "binance", dp.DataSource)
	assert.Equal(t, "kline", dp.DataType)
	assert.False(t, dp.CreatedAt.IsZero())
}
