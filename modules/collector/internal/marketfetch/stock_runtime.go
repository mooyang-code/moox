package marketfetch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/baidu"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	tdxsource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tdx"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
)

func NewStockKlinePipeline(storage Storage) (*KlinePipeline, error) {
	providerID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER")))
	sourceID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SOURCE_ID")))
	if providerID != "" || sourceID != "" {
		if providerID == "" || sourceID == "" {
			return nil, fmt.Errorf("stock_cn source-bound runtime requires provider and source_id")
		}
		return NewStockKlinePipelineForSource(storage, providerID, sourceID)
	}
	route, err := loadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := loadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	for _, providerID := range route.KlineProviders() {
		providerConfig, ok := providerConfigs[providerID]
		if !ok || !providerConfig.KlineEnabled || providerConfig.KlineShadow {
			return nil, fmt.Errorf("stock_cn kline provider %q is not active in source config", providerID)
		}
		provider, providerErr := newStockCNProvider(providerID, providerConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(route.KlineProviders()), nil, nil)
	if err != nil {
		return nil, err
	}
	chain := configuredStockCNProviderChain("MOOX_MARKET_FETCH_PROVIDER_CHAIN", route.KlineProviders())
	if len(chain) == 0 {
		return nil, fmt.Errorf("stock_cn kline provider chain is empty")
	}
	if err := validateStockCNProviderChain(chain, providerConfigs, true); err != nil {
		return nil, err
	}
	calendar, err := loadStockCNCalendar()
	if err != nil {
		return nil, err
	}
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, AutoBindSource: true, RouteID: route.RouteID, SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, DatasetID: StockCNDatasetID, Calendar: calendar, SettleDelay: 5 * time.Second}, nil
}

func NewStockKlinePipelineForSource(storage Storage, providerID, sourceID string) (*KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("stock_cn kline storage is required")
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	if providerID == "" || sourceID == "" {
		return nil, fmt.Errorf("stock_cn source-bound runtime requires provider and source_id")
	}
	route, err := loadStockCNRoute()
	if err != nil {
		return nil, err
	}
	var source stockCNSource
	found := false
	for _, candidate := range route.KlineSources() {
		if candidate.Provider == providerID && candidate.SourceID == sourceID {
			source = candidate
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("stock_cn source %s/%s is not an active route source", providerID, sourceID)
	}
	providerConfigs, err := loadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	providerConfig, ok := providerConfigs[providerID]
	if !ok || !providerConfig.KlineEnabled || providerConfig.KlineShadow {
		return nil, fmt.Errorf("stock_cn source %s/%s is not active in source config", providerID, sourceID)
	}
	provider, err := newStockCNProviderForSource(providerID, sourceID, providerConfig)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, err
	}
	router, err := marketdata.NewRouter(registry, 2, nil, nil)
	if err != nil {
		return nil, err
	}
	calendar, err := loadStockCNCalendar()
	if err != nil {
		return nil, err
	}
	return &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{source.Provider},
		RouteID: route.RouteID, SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID,
		ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity,
		DatasetID: StockCNDatasetID, SourceID: source.SourceID, Calendar: calendar,
		SettleDelay: 5 * time.Second,
	}, nil
}

// NewCryptoKlinePipeline is the crypto composition root for the same common
// KlinePipeline used by stock_cn. Binance remains responsible only for HTTP
// protocol parsing and normalization; Storage and routing stay market-agnostic.
func NewCryptoKlinePipeline(storage Storage, productType marketdata.ProductType) (*KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("crypto kline storage is required")
	}
	if productType == "" {
		productType = marketdata.ProductSpot
	}
	adapter := binance.NewMarketDataAdapter(binance.AdapterConfig{ProductType: productType})
	registry := marketdata.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		return nil, fmt.Errorf("register Binance kline provider: %w", err)
	}
	router, err := marketdata.NewRouter(registry, 2, nil, nil)
	if err != nil {
		return nil, err
	}
	instrumentType := marketdata.InstrumentSpot
	routeID := "binance_spot_kline_1m"
	chain := []string{"binance"}
	if productType == marketdata.ProductSwap {
		instrumentType = marketdata.InstrumentSwap
		routeID = "binance_swap_kline_1m"
	}
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, RouteID: routeID, SpaceID: "crypto", MarketID: "crypto", ProductType: productType, InstrumentType: instrumentType}, nil
}

func loadStockCNCalendar() (*stockmarket.Calendar, error) {
	_, sourceFile, _, _ := runtime.Caller(0)
	sourceRelative := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "config", "markets", "stock_cn", "calendar.yaml"))
	candidates := []string{strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_CALENDAR_PATH")), "markets/stock_cn/calendar.yaml", "config/markets/stock_cn/calendar.yaml", "modules/collector/config/markets/stock_cn/calendar.yaml", sourceRelative}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Clean(candidate)); err != nil {
			continue
		}
		calendar, err := stockmarket.LoadCalendar(candidate)
		if err != nil {
			return nil, fmt.Errorf("load stock_cn calendar %s: %w", candidate, err)
		}
		return calendar, nil
	}
	return nil, fmt.Errorf("stock_cn calendar config was not found")
}

func NewStockInstrumentPipeline(storage InstrumentStorage) (*InstrumentPipeline, error) {
	route, err := loadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := loadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	for _, providerID := range route.InstrumentProviders() {
		providerConfig, ok := providerConfigs[providerID]
		if !ok || !providerConfig.InstrumentEnabled {
			return nil, fmt.Errorf("stock_cn instrument provider %q is not active in source config", providerID)
		}
		provider, providerErr := newStockCNProvider(providerID, providerConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	chain := configuredStockCNProviderChain("MOOX_INSTRUMENT_PROVIDER_CHAIN", route.InstrumentProviders())
	if len(chain) == 0 {
		return nil, fmt.Errorf("stock_cn instrument provider chain is empty")
	}
	if err := validateStockCNProviderChain(chain, providerConfigs, false); err != nil {
		return nil, err
	}
	return &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: chain, InstrumentProviderTimeout: 2 * time.Minute, SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}, MinimumCount: 4000, RouteID: "stock_cn_instrument_v1"}, nil
}

func validateStockCNProviderChain(chain []string, providers map[string]stockCNProviderRuntime, kline bool) error {
	for _, providerID := range chain {
		config, ok := providers[strings.ToLower(strings.TrimSpace(providerID))]
		if !ok {
			return fmt.Errorf("stock_cn provider chain contains unconfigured provider %q", providerID)
		}
		if kline && (!config.KlineEnabled || config.KlineShadow) {
			return fmt.Errorf("stock_cn kline provider %q is not active", providerID)
		}
		if !kline && !config.InstrumentEnabled {
			return fmt.Errorf("stock_cn instrument provider %q is not active", providerID)
		}
	}
	return nil
}

func newStockCNProvider(providerID string, config stockCNProviderRuntime) (marketdata.MarketProvider, error) {
	sourceID := "stock_cn_http"
	if strings.EqualFold(strings.TrimSpace(providerID), "sina") {
		sourceID = "stock_cn_minute_http"
	}
	return newStockCNProviderForSource(providerID, sourceID, config)
}

func newStockCNProviderForSource(providerID, sourceID string, config stockCNProviderRuntime) (marketdata.MarketProvider, error) {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "sina":
		return sina.New(sina.Config{BaseURL: config.KlineBaseURL, KlineEndpoint: config.KlineEndpoint, SourceID: sourceID, InstrumentRequestTimeout: 30 * time.Second, RateLimit: config.RateLimit, MaxBarsPerRequest: config.KlineSpec.MaxBarsPerRequest}), nil
	case "tencent":
		return tencent.New(tencent.Config{BaseURL: config.KlineBaseURL, KlineEndpoint: config.KlineEndpoint, SourceID: sourceID, RateLimit: config.RateLimit, MaxBarsPerRequest: config.KlineSpec.MaxBarsPerRequest}), nil
	case "eastmoney":
		return eastmoney.New(eastmoney.Config{BaseURL: config.KlineBaseURL, KlineEndpoint: config.KlineEndpoint, SourceID: sourceID, InstrumentRequestTimeout: 30 * time.Second, RateLimit: config.RateLimit, MaxBarsPerRequest: config.KlineSpec.MaxBarsPerRequest}), nil
	case "baidu":
		return baidu.New(baidu.Config{SourceID: sourceID, RateLimit: config.RateLimit}), nil
	case "tdx":
		host := ""
		if len(config.Hosts) > 0 {
			host = config.Hosts[0]
		}
		port := config.Port
		if port <= 0 {
			port = 7709
		}
		return tdxsource.New(tdxsource.Config{
			Host: host, Port: port, Timeout: config.RateLimit.RequestTimeout,
			RateLimit: config.RateLimit, MaxBarsPerRequest: config.KlineSpec.MaxBarsPerRequest,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported stock_cn provider %q", providerID)
	}
}

func configuredStockCNProviderChain(envKey string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return append([]string(nil), fallback...)
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' })
	chain := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		provider := strings.ToLower(strings.TrimSpace(part))
		if provider == "" {
			continue
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		chain = append(chain, provider)
	}
	return chain
}

// NewMarketInstrumentPipeline selects a market composition root while keeping
// snapshot validation, active-set switching, and Storage writes in the common
// InstrumentPipeline. A stock snapshot is deliberately not allowed to fall
// through to Binance's product-type mapping.
func NewMarketInstrumentPipeline(storage InstrumentStorage, marketID string, productType marketdata.ProductType) (*InstrumentPipeline, error) {
	if strings.EqualFold(strings.TrimSpace(marketID), StockCNSpaceID) {
		return NewStockInstrumentPipeline(storage)
	}
	return NewCryptoInstrumentPipeline(storage, productType)
}

// NewCryptoInstrumentPipeline composes Binance's typed InstrumentFetcher with
// the same snapshot/active-set pipeline used by stock_cn. The target dataset is
// intentionally optional for symbol snapshots: callers can stage the complete
// instrument membership without implicitly activating a K-line dataset.
func NewCryptoInstrumentPipeline(storage InstrumentStorage, productType marketdata.ProductType) (*InstrumentPipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("crypto instrument storage is required")
	}
	if productType == "" {
		productType = marketdata.ProductSpot
	}
	adapter := binance.NewMarketDataAdapter(binance.AdapterConfig{ProductType: productType})
	registry := marketdata.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		return nil, fmt.Errorf("register Binance instrument provider: %w", err)
	}
	datasetID := "binance_spot_symbols"
	instrumentType := "spot"
	if productType == marketdata.ProductSwap {
		datasetID = "binance_swap_symbols"
		instrumentType = "swap"
	}
	return &InstrumentPipeline{
		Registry: registry, Storage: storage, CandidateChain: []string{"binance"},
		SpaceID: "crypto", MarketID: "crypto", DatasetID: datasetID, DataSourceID: "binance",
		SubjectType: "crypto_pair", SubjectMarket: "CRYPTO", Currency: "USDT", Timezone: "UTC",
		InstrumentType: instrumentType, RequiredExchanges: []string{"binance"}, MinimumCount: 1, RouteID: instrumentRouteID("crypto", instrumentType),
	}, nil
}

// NewMarketStorage keeps one authenticated Storage adapter for both market
// modules while the remaining Binance-specific configuration is retired.
func NewMarketStorage(target, writeSource string) (Storage, error) {
	storage, err := marketstorage.NewBatchStorageWithWriteSource(target, marketstorage.InstTypeSPOT, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}

// NewMarketStorageForMarket is the composition-root storage factory. Storage
// authentication is shared by all market runtimes, while the stock_cn market
// uses the stock binding instead of interpreting "equity" as a Binance
// product type.
func NewMarketStorageForMarket(target, marketType, writeSource string) (StorageReader, error) {
	var storage Storage
	if strings.EqualFold(strings.TrimSpace(marketType), "equity") || strings.EqualFold(strings.TrimSpace(marketType), StockCNSpaceID) {
		var err error
		storage, err = NewMarketStorage(target, writeSource)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		storage, err = marketstorage.NewBatchStorageWithWriteSource(target, marketType, writeSource)
		if err != nil {
			return nil, fmt.Errorf("create %s market storage: %w", strings.TrimSpace(marketType), err)
		}
	}
	reader, ok := storage.(StorageReader)
	if !ok {
		return nil, fmt.Errorf("market storage %q does not support scheduler reads", strings.TrimSpace(marketType))
	}
	return reader, nil
}

func NewStockInstrumentStorage(target, writeSource string) (InstrumentStorage, error) {
	storage, err := marketstorage.NewBatchStorageWithWriteSource(target, marketstorage.InstTypeSPOT, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create stock instrument storage: %w", err)
	}
	return storage, nil
}

func NewCryptoInstrumentStorage(target, marketType, writeSource string) (InstrumentStorage, error) {
	marketType = strings.ToLower(strings.TrimSpace(marketType))
	if marketType != string(marketdata.ProductSpot) && marketType != string(marketdata.ProductSwap) {
		marketType = string(marketdata.ProductSpot)
	}
	storage, err := marketstorage.NewBatchStorageWithWriteSource(target, marketType, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create crypto instrument storage: %w", err)
	}
	instrumentStorage, ok := storage.(InstrumentStorage)
	if !ok {
		return nil, fmt.Errorf("crypto storage %q does not support instrument snapshots", marketType)
	}
	return instrumentStorage, nil
}
