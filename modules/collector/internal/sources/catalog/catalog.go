// Package catalog 登记 Collector 内置的全部行情源：标的同步、SCF 与本地取数、collector-stream 接收都从这里按能力取用。
// 新增一个交易所：实现 marketdata.MarketProvider 与所需能力接口，在 Default 里加一项登记。
// 新增一个市场：另外实现它的标的规则包（markets/<市场>）并在 marketwiring 里引用，加上 config/markets/<市场>/market.yaml。
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
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoneyselect"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoneyturnover"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sinafinance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sinahfq"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/swsresearch"
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

// routedEntry 是按路由分配的 K 线数据源：这个（市场，品种）的来源由市场的路由文件（route.yaml）按标的分配，
// source_id 由路由给出，运行参数来自数据源配置文件。
func routedEntry(market marketdata.MarketID, instrument marketdata.InstrumentType, provider, source string, factory marketdata.Factory) marketdata.CatalogEntry {
	item := entry(market, instrument, provider, source, factory)
	item.RouteSource = true
	return item
}

func binanceFactory(instrument marketdata.InstrumentType) marketdata.Factory {
	return func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
		return binance.NewMarketDataAdapter(binance.AdapterConfig{HTTPClient: deps.HTTPClient, InstrumentType: instrument, RateLimit: deps.Settings.RateLimit}), nil
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
		// A 股股票：K 线来源按 config/markets/stockcn/route.yaml 分配。
		routedEntry(stockcn, marketdata.InstrumentEquity, "sina", "stockcn_minute_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return sina.New(sina.Config{BaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, InstrumentRequestTimeout: stockCNInstrumentTimeout, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		routedEntry(stockcn, marketdata.InstrumentEquity, "tencent", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return tencent.New(tencent.Config{BaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		routedEntry(stockcn, marketdata.InstrumentEquity, "eastmoney", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			s := deps.Settings
			return eastmoney.New(eastmoney.Config{KlineBaseURL: s.BaseURL, KlineEndpoint: s.KlineEndpoint, SourceID: deps.SourceID, InstrumentRequestTimeout: stockCNInstrumentTimeout, RateLimit: s.RateLimit, MaxBarsPerRequest: s.MaxBarsPerRequest}), nil
		}),
		routedEntry(stockcn, marketdata.InstrumentEquity, "baidu", "stockcn_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return baidu.New(baidu.Config{SourceID: deps.SourceID, RateLimit: deps.Settings.RateLimit}), nil
		}),
		routedEntry(stockcn, marketdata.InstrumentEquity, "tdx", "normal_7709", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
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
		// A 股日线的附加字段来源（复权、行业、财务、换手率、股本）：不取 K 线、不进路由配置，限速参数来自各自的数据源配置文件。
		entry(stockcn, marketdata.InstrumentEquity, "sina_hfq", "hfq_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return sinahfq.New(sinahfq.Config{RateLimit: deps.Settings.RateLimit}), nil
		}),
		entry(stockcn, marketdata.InstrumentEquity, "swsresearch", "sw_class_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return swsresearch.New(swsresearch.Config{RateLimit: deps.Settings.RateLimit}), nil
		}),
		entry(stockcn, marketdata.InstrumentEquity, "sina_finance", "finance_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return sinafinance.New(sinafinance.Config{RateLimit: deps.Settings.RateLimit}), nil
		}),
		entry(stockcn, marketdata.InstrumentEquity, "eastmoney_turnover", "turnover_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return eastmoneyturnover.New(eastmoneyturnover.Config{RateLimit: deps.Settings.RateLimit}), nil
		}),
		entry(stockcn, marketdata.InstrumentEquity, "eastmoney_select", "select_http", func(deps marketdata.Deps) (marketdata.MarketProvider, error) {
			return eastmoneyselect.New(eastmoneyselect.Config{RateLimit: deps.Settings.RateLimit}), nil
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

var (
	offersOnce sync.Once
	offers     marketdata.FieldOffers
)

// FieldOffers 返回内置目录里全部可用的附加字段（进程内只算一次）。
func FieldOffers() marketdata.FieldOffers {
	offersOnce.Do(func() {
		built, err := Default().FieldOffers()
		if err != nil {
			panic(err)
		}
		offers = built
	})
	return offers
}
