package binance

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/require"
)

func TestToSubjectIDAllowsChineseBaseAsset(t *testing.T) {
	got, err := ToSubjectID(&exchange.SymbolInfo{Symbol: "币安人生USDT", BaseAsset: "币安人生", QuoteAsset: "USDT"})
	require.NoError(t, err)
	require.Equal(t, "币安人生-USDT", got)
}

func TestToSymbolAllowsChineseSubject(t *testing.T) {
	got, err := ToSymbol("币安人生-USDT")
	require.NoError(t, err)
	require.Equal(t, "币安人生USDT", got)
}
