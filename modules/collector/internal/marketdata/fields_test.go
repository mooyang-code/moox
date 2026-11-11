package marketdata

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// catalogFieldSource 是测试用的字段来源：specs 为空表示这个实例不提供附加字段。
type catalogFieldSource struct {
	id    string
	specs []FieldSpec
	deps  Deps
}

func (s catalogFieldSource) Descriptor() ProviderDescriptor { return ProviderDescriptor{ID: s.id} }
func (s catalogFieldSource) FieldSpecs() []FieldSpec        { return s.specs }
func (s catalogFieldSource) FetchFields(context.Context, FieldRequest) (map[time.Time]map[string]FieldValue, error) {
	return nil, nil
}

type plainProvider struct{ id string }

func (p plainProvider) Descriptor() ProviderDescriptor { return ProviderDescriptor{ID: p.id} }

func fieldCatalog() *Catalog {
	fieldEntry := func(market MarketID, instrument InstrumentType, provider string, specs ...FieldSpec) CatalogEntry {
		return CatalogEntry{Key: SourceKey{ProviderID: provider, SourceID: "http"}, MarketID: market, Instrument: instrument,
			New: func(deps Deps) (MarketProvider, error) {
				return catalogFieldSource{id: provider, specs: specs, deps: deps}, nil
			}}
	}
	daily := []string{"1d"}
	return new(Catalog).MustRegister(
		// K 线来源：source_id 由路由给出，不提供附加字段。
		CatalogEntry{Key: SourceKey{ProviderID: "sina", SourceID: "stockcn_minute_http"}, MarketID: "stockcn", Instrument: InstrumentEquity, RouteSource: true,
			New: func(Deps) (MarketProvider, error) { return plainProvider{id: "sina"}, nil }},
		// 只提供附加字段的来源：独立的提供方 ID，不标 RouteSource。
		fieldEntry("stockcn", InstrumentEquity, "sina_hfq", FieldSpec{ID: "adj_factor", Type: FieldTypeDouble, Fill: "prev", Frequencies: daily}),
		fieldEntry("stockcn", InstrumentEquity, "swsresearch", FieldSpec{ID: "sw_l1", Type: FieldTypeInt, Fill: "prev", Frequencies: daily}, FieldSpec{ID: "sw_l2", Type: FieldTypeInt, Fill: "prev", Frequencies: daily}),
		// 同一类型的适配器，按实例决定有没有字段：永续有，现货没有。
		fieldEntry("crypto", InstrumentSwap, "binance", FieldSpec{ID: "funding_rate", Type: FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}),
		fieldEntry("crypto", InstrumentSpot, "binance"),
	)
}

// 字段来源用独立的提供方 ID 登记，与同一市场的 K 线来源互不影响；按（市场, 品种）找出全部来源，再按字段 ID 找。
func TestCatalogFieldSourcesByMarketAndField(t *testing.T) {
	catalog := fieldCatalog()
	entry, err := catalog.Lookup("stockcn", InstrumentEquity, "sina", "anything")
	require.NoError(t, err)
	require.True(t, entry.RouteSource, "K 线来源仍按 provider 匹配")
	entry, err = catalog.Lookup("stockcn", InstrumentEquity, "sina_hfq", "")
	require.NoError(t, err)
	require.False(t, entry.RouteSource)

	var asked []string
	deps := func(entry CatalogEntry) (Deps, error) {
		asked = append(asked, entry.Key.ProviderID)
		return Deps{Settings: SourceSettings{RateLimit: RateLimitPolicy{RequestsPerSecond: 2}}}, nil
	}
	sources, err := catalog.FieldSources("StockCN", InstrumentEquity, deps)
	require.NoError(t, err)
	require.Len(t, sources, 2, "K 线来源不算字段来源")
	require.Equal(t, []string{"sina", "sina_hfq", "swsresearch"}, asked, "运行参数按提供方 ID 给出")
	source, ok := FindFieldSource(sources, "sw_l2")
	require.True(t, ok)
	require.Equal(t, "swsresearch", source.Descriptor().ID)
	require.Equal(t, 2.0, source.(catalogFieldSource).deps.Settings.RateLimit.RequestsPerSecond)
	_, ok = FindFieldSource(sources, "funding_rate")
	require.False(t, ok)

	spot, err := catalog.FieldSources("crypto", InstrumentSpot, deps)
	require.NoError(t, err)
	require.Empty(t, spot, "现货实例不提供资金费率")
	swap, err := catalog.FieldSources("crypto", InstrumentSwap, deps)
	require.NoError(t, err)
	require.Len(t, swap, 1)
}

func TestCatalogFieldOffers(t *testing.T) {
	offers, err := fieldCatalog().FieldOffers()
	require.NoError(t, err)
	require.Len(t, offers, 4)
	offer, ok := offers.Find("stockcn", "equity", "ADJ_FACTOR")
	require.True(t, ok)
	require.Equal(t, "sina_hfq", offer.ProviderID)
	_, ok = offers.Find("crypto", "spot", "funding_rate")
	require.False(t, ok)
	_, ok = offers.Spec("crypto", "funding_rate")
	require.True(t, ok)
	_, ok = offers.Spec("stockcn", "funding_rate")
	require.False(t, ok, "字段只在自己的市场里")
	require.Len(t, offers.Specs("stockcn"), 3)

	spec, _ := offers.Spec("crypto", "funding_rate")
	require.True(t, spec.SupportsFrequency("1h"))
	require.True(t, spec.SupportsFrequency("1d"))
	require.False(t, spec.SupportsFrequency("1m"))
	require.False(t, spec.SupportsFrequency("bad"))
}
