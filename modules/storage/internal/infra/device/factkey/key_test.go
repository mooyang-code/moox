package factkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTimeSeriesDataKey_WithDimensions_ShouldEscapeAndJoin(t *testing.T) {
	// 测试场景: 构造含维度的时序 data_key
	// 预期结果: subject|freq|dimHash 三段用 | 连接

	got := BuildTimeSeriesDataKey("BTC|USDT", "1m", map[string]string{"exchange": "binance"})

	subject, freq, dimHash, err := ParseTimeSeriesDataKey(got)
	require.NoError(t, err)
	assert.Equal(t, "BTC|USDT", subject)
	assert.Equal(t, "1m", freq)
	assert.Equal(t, DimensionsHash(map[string]string{"exchange": "binance"}), dimHash)
}

func TestBuildTimeSeriesDataKey_EmptyDimensions_ShouldUseEmptyHash(t *testing.T) {
	got := BuildTimeSeriesDataKey("BTC-USDT", "1m", nil)
	_, _, dimHash, err := ParseTimeSeriesDataKey(got)
	require.NoError(t, err)
	assert.Equal(t, emptyDimensionsHash, dimHash)
}

func TestParseTimeSeriesDataKey_InvalidParts_ShouldReturnError(t *testing.T) {
	_, _, _, err := ParseTimeSeriesDataKey("only|two")
	assert.Error(t, err)
}

func TestBuildRecordDataKey_EmptyRecordID_ShouldReturnError(t *testing.T) {
	_, err := BuildRecordDataKey("  ")
	assert.Error(t, err)
}

func TestBuildRecordDataKey_ValidID_ShouldEscapeAndRoundTrip(t *testing.T) {
	key, err := BuildRecordDataKey("rec|100")
	require.NoError(t, err)
	assert.Equal(t, "rec|100", ParseRecordDataKey(key))
}

func TestNormalizeVersion_Empty_ShouldReturnEmptyVersion(t *testing.T) {
	assert.Equal(t, EmptyVersion, NormalizeVersion(""))
	assert.Equal(t, EmptyVersion, NormalizeVersion("   "))
}

func TestNormalizeVersion_RFC3339_ShouldNormalizeToTimeLayout(t *testing.T) {
	got := NormalizeVersion("2026-07-08T06:12:00Z")
	assert.Equal(t, "2026-07-08T06:12:00.000000000Z", got)
}

func TestNormalizeVersion_NonTime_ShouldEscape(t *testing.T) {
	got := NormalizeVersion("v1|beta")
	assert.Equal(t, "v1%7Cbeta", got)
}

func TestNormalizeTimeVersion_Empty_ShouldReturnError(t *testing.T) {
	_, err := NormalizeTimeVersion("")
	assert.Error(t, err)
}

func TestNormalizeTimeVersion_Invalid_ShouldReturnError(t *testing.T) {
	_, err := NormalizeTimeVersion("not-a-time")
	assert.Error(t, err)
}

func TestEscapePart_AndUnescapePart_ShouldRoundTrip(t *testing.T) {
	raw := "a%b|c"
	escaped := EscapePart(raw)
	assert.Equal(t, "a%25b%7Cc", escaped)
	assert.Equal(t, raw, UnescapePart(escaped))
}
