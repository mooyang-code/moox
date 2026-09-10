package binance

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestAdapterSourceIdentityMatchesProduct(t *testing.T) {
	for _, product := range []marketdata.ProductType{marketdata.ProductSpot, marketdata.ProductSwap} {
		t.Run(string(product), func(t *testing.T) {
			adapter := NewMarketDataAdapter(AdapterConfig{ProductType: product})
			require.Equal(t, string(product)+"_http", adapter.Descriptor().SourceID)
		})
	}
}

func TestAdapterRejectsCrossProductAndUnknownSourceBeforeHTTP(t *testing.T) {
	adapter := NewMarketDataAdapter(AdapterConfig{ProductType: marketdata.ProductSwap})
	for _, source := range []string{"spot_http", "unknown"} {
		_, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
			MarketID: "crypto", ExchangeID: "binance", ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap,
			SubjectID: "BTC-USDT-SWAP", ProviderSymbol: "BTCUSDT", SourceID: source, Frequency: "1m", Limit: 1, RequestID: "request",
		})
		require.ErrorIs(t, err, marketdata.ErrInvalidRequest)
	}
}
