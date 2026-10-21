// Package catalog 登记 Collector 内置的全部行情源：标的同步、SCF 与本地取数、collector-stream 接收都从这里按能力取用。
// 新增一个交易所：实现 marketdata.MarketProvider 与所需能力接口，在 Default 里加一项登记。
package catalog

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	bondeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/bond/eastmoney"
	bondsina "github.com/mooyang-code/moox/modules/collector/internal/sources/bond/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/index/cni"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/index/csindex"
	indexeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/index/eastmoney"
	indexsina "github.com/mooyang-code/moox/modules/collector/internal/sources/index/sina"
	indexsw "github.com/mooyang-code/moox/modules/collector/internal/sources/index/sw"
	indextencent "github.com/mooyang-code/moox/modules/collector/internal/sources/index/tencent"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/baidu"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tdx"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
)

const (
	crypto  = marketdata.MarketID("crypto")
	stockcn = marketdata.MarketID("stockcn")
)

// stockCNInstrumentTimeout 是 A 股标的快照单次请求超时（全市场快照分页多）。
const stockCNInstrumentTimeout = 30 * time.Second

var (
	once    sync.Once
	builtIn *marketdata.Catalog
)

// Default 返回内置目录（进程内只构造一次，登记项不可变）。
func Default() *marketdata.Catalog {
	once.Do(func() { builtIn = build() })
	return builtIn
}

func entry(market marketdata.MarketID, instrument marketdata.InstrumentType, provider, source string, factory marketdata.Factory) marketdata.CatalogEntry {
	return marketdata.CatalogEntry{Key: marketdata.SourceKey{ProviderID: provider, SourceID: source}, MarketID: market, Instrument: instrument, New: factory}
}

// stockCNEntry 是 A 股股票数据源：source_id 由路由配置给出，运行参数来自数据源配置文件。
func stockCNEntry(provider, source string, factory marketdata.Factory) marketdata.CatalogEntry {
	item := entry(stockcn, marketdata.InstrumentEquity, provider, source, factory)
	item.RouteSource = true
	return item
}

func binanceFactory(instrument marketdata.InstrumentType) marketdata.Factory {
	return func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
		return binance.NewMarketDataAdapter(binance.AdapterConfig{HTTPClient: deps.HTTPClient, InstrumentType: instrument}), nil
	}
}

func fixed(build func() marketdata.MarketProvider) marketdata.Factory {
	return func(marketdata.Deps) (marketdata.MarketProvider, error) { return build(), nil }
}

func build() *marketdata.Catalog {
	return new(marketdata.Catalog).MustRegister(
		// 加密：币安现货与 U 本位永续（REST K 线、标的快照、WS 推送）。
		entry(crypto, marketdata.InstrumentSpot, "binance", "spot_http", binanceFactory(marketdata.InstrumentSpot)),
		entry(crypto, marketdata.InstrumentSwap, "binance", "swap_http", binanceFactory(marketdata.InstrumentSwap)),
		// A 股股票。
		stockCNEntry("sina", "stockcn_minute_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return sina.New(sina.Config{BaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, InstrumentRequestTimeout: stockCNInstrumentTimeout, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		stockCNEntry("tencent", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return tencent.New(tencent.Config{BaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		stockCNEntry("eastmoney", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return eastmoney.New(eastmoney.Config{BaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, InstrumentRequestTimeout: stockCNInstrumentTimeout, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		stockCNEntry("baidu", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return baidu.New(baidu.Config{SourceID: deps.SourceID, RateLimit: deps.Settings.RateLimit}), nil
		}),
		stockCNEntry("tdx", "normal_7709", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			host := ""
			if len(s.Hosts) > 0 {
				host = s.Hosts[0]
			}
			port := s.Port
			if port <= 0 {
				port = 7709
			}
			return tdx.New(tdx.Config{Host: host, Port: port, Timeout: s.RateLimit.RequestTimeout, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		// A 股可转债。
		entry(stockcn, marketdata.InstrumentConvertibleBond, "eastmoney", "convertible_bond_http", fixed(func() marketdata.MarketProvider { return bondeastmoney.New(bondeastmoney.Config{}) })),
		entry(stockcn, marketdata.InstrumentConvertibleBond, "sina", "convertible_bond_http", fixed(func() marketdata.MarketProvider { return bondsina.New(bondsina.Config{}) })),
		// A 股指数。
		entry(stockcn, marketdata.InstrumentIndex, "eastmoney", "index_http", fixed(func() marketdata.MarketProvider { return indexeastmoney.New(indexeastmoney.Config{}) })),
		entry(stockcn, marketdata.InstrumentIndex, "sina", "index_http", fixed(func() marketdata.MarketProvider { return indexsina.New(indexsina.Config{}) })),
		entry(stockcn, marketdata.InstrumentIndex, "tencent", "index_http", fixed(func() marketdata.MarketProvider { return indextencent.New(indextencent.Config{}) })),
		entry(stockcn, marketdata.InstrumentIndex, "cni", "index_cni_http", fixed(func() marketdata.MarketProvider { return cni.New(cni.Config{}) })),
		entry(stockcn, marketdata.InstrumentIndex, "sw", "index_sw_http", fixed(func() marketdata.MarketProvider { return indexsw.New(indexsw.Config{}) })),
		entry(stockcn, marketdata.InstrumentIndex, "csindex", "index_http", fixed(func() marketdata.MarketProvider { return csindex.New(csindex.Config{}) })),
	)
}
