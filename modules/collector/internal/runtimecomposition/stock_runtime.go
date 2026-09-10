package runtimecomposition

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/baidu"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	tdxsource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tdx"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
)

func NewStockKlinePipeline(storage marketfetch.Storage) (*marketfetch.KlinePipeline, error) {
	providerID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER")))
	sourceID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SOURCE_ID")))
	if providerID != "" || sourceID != "" {
		if providerID == "" || sourceID == "" {
			return nil, fmt.Errorf("stockcn source-bound runtime requires provider and source_id")
		}
		return NewStockKlinePipelineForSource(storage, providerID, sourceID)
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	for _, providerID := range route.KlineProviders() {
		providerConfig, ok := providerConfigs[providerID]
		if !ok || !providerConfig.KlineEnabled || providerConfig.KlineShadow {
			return nil, fmt.Errorf("stockcn kline provider %q is not active in source config", providerID)
		}
		provider, providerErr := newStockCNProvider(providerID, providerConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(route.KlineProviders())*marketfetch.KlineProviderAttemptBudget, nil, nil)
	if err != nil {
		return nil, err
	}
	chain := configuredStockCNProviderChain("MOOX_MARKET_FETCH_PROVIDER_CHAIN", route.KlineProviders())
	if len(chain) == 0 {
		return nil, fmt.Errorf("stockcn kline provider chain is empty")
	}
	if err := validateStockCNProviderChain(chain, providerConfigs, true); err != nil {
		return nil, err
	}
	calendar, err := loadStockCNCalendar()
	if err != nil {
		return nil, err
	}
	return &marketfetch.KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, AutoBindSource: true, RouteID: route.RouteID, SpaceID: marketfetch.StockCNSpaceID, MarketID: marketfetch.StockCNSpaceID, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, DatasetID: marketfetch.StockCNDatasetID, Calendar: calendar, SettleDelay: 5 * time.Second}, nil
}

func NewStockKlinePipelineForSource(storage marketfetch.Storage, providerID, sourceID string) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("stockcn kline storage is required")
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	if providerID == "" || sourceID == "" {
		return nil, fmt.Errorf("stockcn source-bound runtime requires provider and source_id")
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	found := false
	for _, candidate := range route.KlineSources() {
		if candidate.Provider == providerID && candidate.SourceID == sourceID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("stockcn source %s/%s is not an active route source", providerID, sourceID)
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	providerConfig, ok := providerConfigs[providerID]
	if !ok || !providerConfig.KlineEnabled || providerConfig.KlineShadow {
		return nil, fmt.Errorf("stockcn source %s/%s is not active in source config", providerID, sourceID)
	}
	registry := marketdata.NewRegistry()
	for _, candidateID := range route.KlineProviders() {
		candidateConfig, candidateOK := providerConfigs[candidateID]
		if !candidateOK || !candidateConfig.KlineEnabled || candidateConfig.KlineShadow {
			return nil, fmt.Errorf("stockcn kline provider %q is not active in source config", candidateID)
		}
		candidateSourceID := stockCNSourceID(route, candidateID)
		candidate, providerErr := newStockCNProviderForSource(candidateID, candidateSourceID, candidateConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(candidate); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(route.KlineProviders())*marketfetch.KlineProviderAttemptBudget, nil, nil)
	if err != nil {
		return nil, err
	}
	calendar, err := loadStockCNCalendar()
	if err != nil {
		return nil, err
	}
	return &marketfetch.KlinePipeline{
		Router: router, Storage: storage, CandidateChain: route.KlineProviders(),
		RouteID: route.RouteID, SpaceID: marketfetch.StockCNSpaceID, MarketID: marketfetch.StockCNSpaceID,
		ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity,
		DatasetID: marketfetch.StockCNDatasetID, Calendar: calendar,
		SettleDelay: 5 * time.Second,
	}, nil
}

func stockCNSourceID(route marketfetch.StockCNRoute, providerID string) string {
	for _, source := range route.KlineSources() {
		if source.Provider == strings.ToLower(strings.TrimSpace(providerID)) {
			return source.SourceID
		}
	}
	return ""
}

// NewCryptoKlinePipeline is the crypto composition root for the same common
// KlinePipeline used by stockcn. Binance remains responsible only for HTTP
// protocol parsing and normalization; marketfetch.Storage and routing stay market-agnostic.
func NewCryptoKlinePipeline(storage marketfetch.Storage, productType marketdata.ProductType) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("crypto kline storage is required")
	}
	if productType == "" {
		productType = marketdata.ProductSpot
	}
	if productType != marketdata.ProductSpot && productType != marketdata.ProductSwap {
		return nil, fmt.Errorf("unsupported crypto product %q", productType)
	}
	instrumentType := marketdata.InstrumentSpot
	if productType == marketdata.ProductSwap {
		instrumentType = marketdata.InstrumentSwap
	}
	// Keep the crypto composition root on the same provider/source factory as
	// the configured market routes. This is important for swap: the handler
	// uses this factory for both timer and invoke requests, so a swap request
	// must receive the swap-aware Binance adapter rather than a spot default.
	pipeline, err := NewMarketKlinePipeline(storage, "crypto", instrumentType, "binance", DefaultSourceID("binance", string(instrumentType)))
	if err != nil {
		return nil, fmt.Errorf("create Binance crypto kline pipeline: %w", err)
	}
	// The target dataset is supplied by the active Collector rule (the 1m and
	// 1h datasets are different). Leave it request-bound instead of imposing
	// the legacy route dataset ID selected by NewMarketKlinePipeline.
	pipeline.DatasetID = ""
	pipeline.ProductType = productType
	pipeline.InstrumentType = instrumentType
	return pipeline, nil
}

func loadStockCNCalendar() (*stockmarket.Calendar, error) {
	_, sourceFile, _, _ := runtime.Caller(0)
	sourceRelative := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	candidates := []string{strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_CALENDAR_PATH")), "markets/stockcn/calendar.yaml", "config/markets/stockcn/calendar.yaml", "modules/collector/config/markets/stockcn/calendar.yaml", sourceRelative}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Clean(candidate)); err != nil {
			continue
		}
		calendar, err := stockmarket.LoadCalendar(candidate)
		if err != nil {
			return nil, fmt.Errorf("load stockcn calendar %s: %w", candidate, err)
		}
		return calendar, nil
	}
	return nil, fmt.Errorf("stockcn calendar config was not found")
}

func NewStockInstrumentPipeline(storage marketfetch.InstrumentStorage) (*marketfetch.InstrumentPipeline, error) {
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	for _, providerID := range route.InstrumentProviders() {
		providerConfig, ok := providerConfigs[providerID]
		if !ok || !providerConfig.InstrumentEnabled {
			return nil, fmt.Errorf("stockcn instrument provider %q is not active in source config", providerID)
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
		return nil, fmt.Errorf("stockcn instrument provider chain is empty")
	}
	if err := validateStockCNProviderChain(chain, providerConfigs, false); err != nil {
		return nil, err
	}
	return &marketfetch.InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: chain, InstrumentProviderTimeout: 2 * time.Minute, SpaceID: marketfetch.StockCNSpaceID, MarketID: marketfetch.StockCNSpaceID, DatasetID: marketfetch.StockCNInstrumentDatasetID, TargetDatasetID: marketfetch.StockCNDatasetID, DataSourceID: marketfetch.StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}, MinimumCount: 4000, RouteID: "stockcn_instrument_v1"}, nil
}

func validateStockCNProviderChain(chain []string, providers map[string]marketfetch.StockCNProviderRuntime, kline bool) error {
	for _, providerID := range chain {
		config, ok := providers[strings.ToLower(strings.TrimSpace(providerID))]
		if !ok {
			return fmt.Errorf("stockcn provider chain contains unconfigured provider %q", providerID)
		}
		if kline && (!config.KlineEnabled || config.KlineShadow) {
			return fmt.Errorf("stockcn kline provider %q is not active", providerID)
		}
		if !kline && !config.InstrumentEnabled {
			return fmt.Errorf("stockcn instrument provider %q is not active", providerID)
		}
	}
	return nil
}

func newStockCNProvider(providerID string, config marketfetch.StockCNProviderRuntime) (marketdata.MarketProvider, error) {
	sourceID := "stockcn_http"
	if strings.EqualFold(strings.TrimSpace(providerID), "sina") {
		sourceID = "stockcn_minute_http"
	}
	return newStockCNProviderForSource(providerID, sourceID, config)
}

func newStockCNProviderForSource(providerID, sourceID string, config marketfetch.StockCNProviderRuntime) (marketdata.MarketProvider, error) {
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
		return nil, fmt.Errorf("unsupported stockcn provider %q", providerID)
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
// snapshot validation, active-set switching, and marketfetch.Storage writes in the common
// marketfetch.InstrumentPipeline. A stock snapshot is deliberately not allowed to fall
// through to Binance's product-type mapping.
func NewMarketInstrumentPipeline(storage marketfetch.InstrumentStorage, marketID string, productType marketdata.ProductType) (*marketfetch.InstrumentPipeline, error) {
	if strings.EqualFold(strings.TrimSpace(marketID), marketfetch.StockCNSpaceID) {
		return NewStockInstrumentPipeline(storage)
	}
	if !strings.EqualFold(strings.TrimSpace(marketID), "crypto") {
		return nil, fmt.Errorf("unsupported instrument market %q", marketID)
	}
	return NewCryptoInstrumentPipeline(storage, productType)
}

// NewCryptoInstrumentPipeline composes Binance's typed InstrumentFetcher with
// the same snapshot/active-set pipeline used by stockcn. The target dataset is
// intentionally optional for symbol snapshots: callers can stage the complete
// instrument membership without implicitly activating a K-line dataset.
func NewCryptoInstrumentPipeline(storage marketfetch.InstrumentStorage, productType marketdata.ProductType) (*marketfetch.InstrumentPipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("crypto instrument storage is required")
	}
	if productType == "" {
		productType = marketdata.ProductSpot
	}
	if productType != marketdata.ProductSpot && productType != marketdata.ProductSwap {
		return nil, fmt.Errorf("unsupported crypto product %q", productType)
	}
	adapter := binance.NewMarketDataAdapter(binance.AdapterConfig{ProductType: productType})
	registry := marketdata.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		return nil, fmt.Errorf("register Binance instrument provider: %w", err)
	}
	datasetID := "dataset_binance_spot_symbols"
	instrumentType := "spot"
	if productType == marketdata.ProductSwap {
		datasetID = "dataset_binance_swap_symbols"
		instrumentType = "swap"
	}
	return &marketfetch.InstrumentPipeline{
		Registry: registry, Storage: storage, CandidateChain: []string{"binance"},
		SpaceID: "crypto", MarketID: "crypto", DatasetID: datasetID, DataSourceID: "binance",
		SubjectType: "crypto_pair", SubjectMarket: "CRYPTO", Currency: "USDT", Timezone: "UTC",
		InstrumentType: instrumentType, RequiredExchanges: []string{"binance"}, MinimumCount: 1, RouteID: marketfetch.InstrumentRouteID("crypto", instrumentType),
	}, nil
}
