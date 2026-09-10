package marketfetch

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestCryptoCompositionRootSupportsMinuteAndHourMarkets(t *testing.T) {
	for _, product := range []marketdata.ProductType{marketdata.ProductSpot, marketdata.ProductSwap} {
		for _, frequency := range []string{"1m", "1H"} {
			t.Run(string(product)+"/"+frequency, func(t *testing.T) {
				pipeline, err := NewCryptoKlinePipeline(timerHandlerStorage{}, product)
				require.NoError(t, err)
				require.Empty(t, pipeline.DatasetID)
				require.Empty(t, pipeline.SourceID)
				require.Equal(t, []string{"binance"}, pipeline.CandidateChain)
				require.Equal(t, product, pipeline.ProductType)
				spec, err := pipeline.Router.NewSession().KlineSpec("binance")
				require.NoError(t, err)
				require.True(t, spec.SupportsRequest(marketdata.KlineRequest{
					MarketID: "crypto", ExchangeID: "binance", ProductType: product,
					InstrumentType: marketdata.InstrumentType(product), Frequency: frequency,
				}))
			})
		}
	}
}
