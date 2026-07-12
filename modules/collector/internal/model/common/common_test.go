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

func TestNewDecimalFromFloat_ShouldFormat(t *testing.T) {
	d := NewDecimalFromFloat(1.5)
	assert.Equal(t, "1.50000000", d.String())
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

func TestZero_ShouldBeZeroString(t *testing.T) {
	assert.Equal(t, "0", Zero().String())
}

func TestNewBaseDataPoint_ShouldPopulateFields(t *testing.T) {
	dp := NewBaseDataPoint("binance", "kline")
	assert.Equal(t, "binance", dp.Source())
	assert.Equal(t, "kline", dp.SourceType())
	assert.False(t, dp.Timestamp().IsZero())
	assert.NoError(t, dp.Validate())

	raw, err := dp.Marshal()
	require.NoError(t, err)
	var restored BaseDataPoint
	require.NoError(t, restored.Unmarshal(raw))
	assert.Equal(t, dp.DataSource, restored.DataSource)
}
