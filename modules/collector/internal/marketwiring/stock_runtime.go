package marketwiring

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

// NewStockKlinePipeline 构造 A 股股票某个频率的 K 线流水线：候选链取路由里该频率的配置，
// 来源按标的在 primary 之间分配，失败后按候选链依次兜底。
func NewStockKlinePipeline(storage marketfetch.Storage, frequency string) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("stockcn K 线流水线缺少 Storage")
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	kline, err := route.Kline(frequency)
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	chain := kline.ProviderChain()
	registry := marketdata.NewRegistry()
	for _, source := range kline.Sources() {
		providerConfig, ok := providerConfigs[source.Provider]
		if !ok || !providerConfig.KlineEnabled || providerConfig.KlineShadow {
			return nil, fmt.Errorf("stockcn K 线来源 %q 在数据源配置里没有启用", source.Provider)
		}
		provider, providerErr := newStockCNProvider(source.Provider, source.SourceID, providerConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(chain)*marketfetch.RuntimeKlineProviderAttemptBudget(), nil, nil)
	if err != nil {
		return nil, err
	}
	return &marketfetch.KlinePipeline{
		Router: router, Storage: storage, CandidateChain: chain, AutoBindSource: true,
		SpaceID: marketfetch.StockCNSpaceID, MarketID: marketfetch.StockCNSpaceID, InstrumentType: marketdata.InstrumentEquity,
		SettleDelay: 5 * time.Second,
	}, nil
}

// NewCryptoKlinePipeline is the crypto composition root for the same common
// KlinePipeline used by stockcn. Binance remains responsible only for HTTP
// protocol parsing and normalization; marketfetch.Storage and routing stay market-agnostic.
func NewCryptoKlinePipeline(storage marketfetch.Storage, productType marketdata.InstrumentType, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("crypto kline storage is required")
	}
	if productType == "" {
		productType = marketdata.InstrumentSpot
	}
	if productType != marketdata.InstrumentSpot && productType != marketdata.InstrumentSwap {
		return nil, fmt.Errorf("unsupported crypto product %q", productType)
	}
	instrumentType := marketdata.InstrumentSpot
	if productType == marketdata.InstrumentSwap {
		instrumentType = marketdata.InstrumentSwap
	}
	// Keep the crypto composition root on the same provider/source factory as
	// the configured market routes. This is important for swap: the handler
	// uses this factory for both timer and invoke requests, so a swap request
	// must receive the swap-aware Binance adapter rather than a spot default.
	pipeline, err := newMarketKlinePipeline(storage, "crypto", instrumentType, "binance", DefaultSourceID("binance", string(instrumentType)), limits)
	if err != nil {
		return nil, fmt.Errorf("create Binance crypto kline pipeline: %w", err)
	}
	// The target dataset is supplied by the active Collector rule (the 1m and
	// 1h datasets are different). Leave it request-bound instead of imposing
	// the default route dataset ID selected by NewMarketKlinePipeline.
	pipeline.DatasetID = ""
	pipeline.InstrumentType = instrumentType
	return pipeline, nil
}

// newStockCNProvider 从行情源目录构造 A 股股票数据源；sourceID 为空时取登记的默认值，运行参数来自数据源配置文件。
func newStockCNProvider(providerID, sourceID string, config marketfetch.StockCNProviderRuntime) (marketdata.MarketProvider, error) {
	entry, err := catalog.Default().Lookup(marketfetch.StockCNSpaceID, marketdata.InstrumentEquity, providerID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unsupported stockcn provider %q", providerID)
	}
	return entry.Build(marketdata.Deps{SourceID: sourceID, Settings: stockCNSettings(config)})
}

// stockCNSettings 把 A 股数据源配置文件的运行参数交给目录里的工厂。
func stockCNSettings(config marketfetch.StockCNProviderRuntime) marketdata.SourceSettings {
	return marketdata.SourceSettings{
		BaseURL: config.KlineBaseURL, KlineEndpoint: config.KlineEndpoint, Hosts: config.Hosts, Port: config.Port,
		MaxBarsPerRequest: config.KlineSpec.MaxBarsPerRequest, RateLimit: config.RateLimit,
	}
}
