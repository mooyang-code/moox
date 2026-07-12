package binance

import (
	"testing"

	exchange "github.com/mooyang-code/moox/modules/collector/internal/sources/exchangetypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizedSubjectID(t *testing.T) {
	assert.Equal(t, "BTC-USDT", normalizedSubjectID(&exchange.SymbolInfo{
		Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}))
	assert.Equal(t, "ETH-USDT", normalizedSubjectID(&exchange.SymbolInfo{
		Symbol: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
	}))
}

func TestContainsHyphen(t *testing.T) {
	assert.True(t, containsHyphen("BTC-USDT"))
	assert.False(t, containsHyphen("BTCUSDT"))
}

func TestAppendOptionalDoubleField(t *testing.T) {
	cols, err := appendOptionalDoubleField(nil, "x", "")
	require.NoError(t, err)
	assert.Nil(t, cols)

	cols, err = appendOptionalDoubleField(nil, "x", "1.25")
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, 1.25, cols[0].GetValue().GetDoubleValue())

	_, err = appendOptionalDoubleField(nil, "x", "bad")
	assert.Error(t, err)
}

func TestBuildSymbolRegisterRequest(t *testing.T) {
	req := buildSymbolRegisterRequest(&exchange.SymbolInfo{
		Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active",
	}, StorageBinding{SpaceID: "crypto", RecordDatasetID: "ds-1", DataSourceID: "binance"})
	assert.Equal(t, "crypto", req.GetSpaceId())
	assert.Equal(t, "BTC-USDT", req.GetSubject().GetSubjectId())
	assert.Equal(t, "binance", req.GetDataSourceId())
}

func TestBuildSymbolRecordRows(t *testing.T) {
	rows, err := buildSymbolRecordRows([]*exchange.SymbolInfo{{
		Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active",
	}}, StorageBinding{SpaceID: "crypto", RecordDatasetID: "ds-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "crypto", rows[0].GetKey().GetSpaceId())
	assert.Equal(t, "ds-1", rows[0].GetKey().GetDatasetId())
	assert.Equal(t, "BTC-USDT", rows[0].GetKey().GetRecordId())
}
