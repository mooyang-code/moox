package marketdata_test

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryExposesBinanceThroughBothTypedFetcherContracts(t *testing.T) {
	registry := marketdata.NewRegistry()
	provider := binance.NewMarketDataAdapter(binance.AdapterConfig{})
	require.NoError(t, registry.Register(provider))

	registered, err := registry.Provider("binance")
	require.NoError(t, err)
	assert.Same(t, provider, registered)

	klineFetcher, err := registry.KlineFetcher("binance")
	require.NoError(t, err)
	assert.Same(t, provider, klineFetcher)

	instrumentFetcher, err := registry.InstrumentFetcher("binance")
	require.NoError(t, err)
	assert.Same(t, provider, instrumentFetcher)
}
