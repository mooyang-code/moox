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
	// 路由项与非路由项同 provider 不同 source_id 时，Lookup 必须精确比对 source_id，不依赖登记顺序。
	require.NoError(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "tencent", SourceID: "stockhk_minute_http"}, MarketID: "stockhk", Instrument: InstrumentEquity, RouteSource: true, New: factory}))
	require.NoError(t, catalog.Register(CatalogEntry{Key: SourceKey{ProviderID: "tencent", SourceID: "index_constituent_http"}, MarketID: "stockhk", Instrument: InstrumentEquity, New: factory}))

	entry, err := catalog.Lookup("crypto", InstrumentSpot, "BINANCE", "")
	require.NoError(t, err)
	require.Equal(t, "spot_http", entry.Key.SourceID)
	_, err = catalog.Lookup("crypto", InstrumentSpot, "binance", "swap_http")
	require.ErrorIs(t, err, ErrProviderNotFound)
	_, err = catalog.Lookup("crypto", InstrumentSwap, "binance", "spot_http")
	require.ErrorIs(t, err, ErrProviderNotFound)
	// 路由项也须匹配 source_id，空 source_id 匹配首个登记项。
	entry, err = catalog.Lookup("stockcn", InstrumentEquity, "tdx", "normal_7709")
	require.NoError(t, err)
	require.Equal(t, "normal_7709", entry.Key.SourceID)
	_, err = catalog.Lookup("stockcn", InstrumentEquity, "tdx", "anything")
	require.ErrorIs(t, err, ErrProviderNotFound)
	entry, err = catalog.Lookup("stockhk", InstrumentEquity, "tencent", "index_constituent_http")
	require.NoError(t, err)
	require.False(t, entry.RouteSource)
	entry, err = catalog.Lookup("stockhk", InstrumentEquity, "tencent", "stockhk_minute_http")
	require.NoError(t, err)
	require.True(t, entry.RouteSource)
	require.True(t, catalog.Has("stockcn", InstrumentEquity))
	require.False(t, catalog.Has("stockcn", InstrumentSpot))
}
