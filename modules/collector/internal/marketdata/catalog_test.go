package marketdata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogRegisterAndLookup(t *testing.T) {
	factory := func(Deps) (MarketProvider, error) { return nil, nil }
	catalog := new(Catalog)
	require.NoError(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "Binance", SourceID: "spot_http"}, MarketID: "crypto", Instrument: InstrumentSpot, New: factory}))
	require.ErrorIs(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "binance", SourceID: "spot_http"}, MarketID: "crypto", Instrument: InstrumentSpot, New: factory}), ErrProviderAlreadyRegistered)
	require.ErrorIs(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "x", SourceID: "y"}, MarketID: "crypto", Instrument: InstrumentSpot}), ErrInvalidRequest)
	require.NoError(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, MarketID: "stockcn", Instrument: InstrumentEquity, RouteSource: true, New: factory}))

	entry, err := catalog.Lookup("crypto", InstrumentSpot, "BINANCE", "")
	require.NoError(t, err)
	require.Equal(t, "spot_http", entry.Key.SourceID)
	_, err = catalog.Lookup("crypto", InstrumentSpot, "binance", "swap_http")
	require.ErrorIs(t, err, ErrProviderNotFound)
	_, err = catalog.Lookup("crypto", InstrumentSwap, "binance", "spot_http")
	require.ErrorIs(t, err, ErrProviderNotFound)
	// source_id 由路由给出的登记项只按 provider 匹配。
	_, err = catalog.Lookup("stockcn", InstrumentEquity, "tdx", "anything")
	require.NoError(t, err)
	require.True(t, catalog.Has("stockcn", InstrumentEquity))
	require.False(t, catalog.Has("stockcn", InstrumentIndex))
}
