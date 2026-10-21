package marketwiring

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
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
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
		provider, providerErr := newStockCNProvider(providerID, "", providerConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(route.KlineProviders())*marketfetch.RuntimeKlineProviderAttemptBudget(), nil, nil)
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
	return &marketfetch.KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, AutoBindSource: true, RouteID: route.RouteID, SpaceID: marketfetch.StockCNSpaceID, MarketID: marketfetch.StockCNSpaceID, InstrumentType: marketdata.InstrumentEquity, DatasetID: marketfetch.StockCNDatasetID, Calendar: calendar, SettleDelay: 5 * time.Second}, nil
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
		candidate, providerErr := newStockCNProvider(candidateID, candidateSourceID, candidateConfig)
		if providerErr != nil {
			return nil, providerErr
		}
		if err := registry.Register(candidate); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, len(route.KlineProviders())*marketfetch.RuntimeKlineProviderAttemptBudget(), nil, nil)
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
		InstrumentType: marketdata.InstrumentEquity,
		DatasetID:      marketfetch.StockCNDatasetID, Calendar: calendar,
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
func NewCryptoKlinePipeline(storage marketfetch.Storage, productType marketdata.InstrumentType) (*marketfetch.KlinePipeline, error) {
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
	pipeline, err := NewMarketKlinePipeline(storage, "crypto", instrumentType, "binance", DefaultSourceID("binance", string(instrumentType)))
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
