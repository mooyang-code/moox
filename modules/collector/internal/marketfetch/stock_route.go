package marketfetch

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"gopkg.in/yaml.v3"
)

// stockCNRoute is the release-time route contract. It is deliberately loaded
// by the SCF composition root instead of being duplicated in provider code.
type stockCNRoute struct {
	MarketID              string                 `yaml:"market_id"`
	RouteID               string                 `yaml:"route_id"`
	Frequency             string                 `yaml:"frequency"`
	TimerFunctionCount    int                    `yaml:"timer_function_count"`
	MeasuredSafeGroupSize int                    `yaml:"measured_safe_group_size"`
	Stagger               stockCNStaggerFile     `yaml:"stagger"`
	Providers             []stockCNRouteProvider `yaml:"providers"`
	History               stockCNHistoryFile     `yaml:"history"`
}

type stockCNStaggerFile struct {
	StartSecond        int `yaml:"start_second"`
	WindowSeconds      int `yaml:"window_seconds"`
	MaxStartsPerSecond int `yaml:"max_starts_per_second"`
}

type stockCNHistoryFile struct {
	Mode        string `yaml:"mode"`
	Backfill    bool   `yaml:"backfill_enabled"`
	MaxLookback string `yaml:"max_lookback"`
}

type stockCNRouteProvider struct {
	ID         string `yaml:"id"`
	SourceID   string `yaml:"source_id"`
	Weight     int    `yaml:"weight"`
	Kline      string `yaml:"kline"`
	KlineRole  string `yaml:"kline_role"`
	Instrument string `yaml:"instrument"`
}

func loadStockCNRouteFile(path string) (stockCNRoute, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stockCNRoute{}, err
	}
	var route stockCNRoute
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&route); err != nil {
		return stockCNRoute{}, err
	}
	if err := route.Validate(); err != nil {
		return stockCNRoute{}, fmt.Errorf("validate stock_cn route: %w", err)
	}
	return route, nil
}

func (r stockCNRoute) Validate() error {
	if strings.TrimSpace(r.MarketID) != StockCNSpaceID {
		return fmt.Errorf("market_id must be %q", StockCNSpaceID)
	}
	if strings.TrimSpace(r.RouteID) == "" || strings.TrimSpace(r.Frequency) == "" {
		return fmt.Errorf("route_id and frequency are required")
	}
	if r.Frequency != "1m" {
		return fmt.Errorf("stock_cn frequency must be 1m")
	}
	if r.TimerFunctionCount < 0 || r.MeasuredSafeGroupSize < 0 {
		return fmt.Errorf("timer_function_count and measured_safe_group_size cannot be negative")
	}
	if raw := strings.TrimSpace(r.History.MaxLookback); raw != "" {
		lookback, err := domain.ParseScheduleInterval(raw)
		if err != nil {
			return fmt.Errorf("history.max_lookback: %w", err)
		}
		if lookback > stockCNHistoryMaxLookback {
			return fmt.Errorf("history.max_lookback %s exceeds current provider capability %s", raw, stockCNHistoryMaxLookback)
		}
	}
	seen := make(map[string]struct{}, len(r.Providers))
	for _, provider := range r.Providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return fmt.Errorf("provider id is required")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("provider %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if provider.Weight <= 0 {
			return fmt.Errorf("provider %q weight must be positive", id)
		}
		if err := validateRouteState(provider.Kline); err != nil {
			return fmt.Errorf("provider %q kline: %w", id, err)
		}
		if err := validateKlineRole(provider.KlineRole); err != nil {
			return fmt.Errorf("provider %q kline_role: %w", id, err)
		}
		if err := validateRouteState(provider.Instrument); err != nil {
			return fmt.Errorf("provider %q instrument: %w", id, err)
		}
	}
	if len(r.KlineProviders()) < 3 {
		return fmt.Errorf("at least three active stock_cn kline providers are required")
	}
	if len(r.KlinePrimaryProviders()) < 2 {
		return fmt.Errorf("at least two primary stock_cn kline providers are required")
	}
	return nil
}

func validateRouteState(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "shadow", "disabled":
		return nil
	default:
		return fmt.Errorf("state must be active, shadow or disabled")
	}
}

func validateKlineRole(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "primary", "fallback":
		return nil
	default:
		return fmt.Errorf("role must be primary or fallback")
	}
}

func (r stockCNRoute) KlineProviders() []string {
	return r.providersFor("kline", "active")
}

func (r stockCNRoute) KlinePrimaryProviders() []string {
	providers := make([]string, 0, len(r.Providers))
	for _, provider := range r.Providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Kline), "active") {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(provider.KlineRole))
		if role == "" || role == "primary" {
			providers = append(providers, strings.TrimSpace(provider.ID))
		}
	}
	return providers
}

func (r stockCNRoute) InstrumentProviders() []string {
	return r.providersFor("instrument", "active")
}

func (r stockCNRoute) KlineWeights() map[string]int {
	weights := make(map[string]int)
	for _, provider := range r.Providers {
		if strings.EqualFold(strings.TrimSpace(provider.Kline), "active") {
			weights[strings.TrimSpace(provider.ID)] = provider.Weight
		}
	}
	return weights
}

type stockCNSource struct {
	Provider string
	SourceID string
	Weight   int
}

func (r stockCNRoute) KlineSources() []stockCNSource {
	return r.klineSources(false)
}

func (r stockCNRoute) KlinePrimarySources() []stockCNSource {
	return r.klineSources(true)
}

func (r stockCNRoute) klineSources(primaryOnly bool) []stockCNSource {
	sources := make([]stockCNSource, 0, len(r.Providers))
	for _, provider := range r.Providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Kline), "active") {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(provider.KlineRole))
		if primaryOnly && role != "" && role != "primary" {
			continue
		}
		sourceID := strings.TrimSpace(provider.SourceID)
		if sourceID == "" {
			sourceID = strings.TrimSpace(provider.ID) + "_http"
		}
		sources = append(sources, stockCNSource{
			Provider: strings.ToLower(strings.TrimSpace(provider.ID)),
			SourceID: sourceID,
			Weight:   provider.Weight,
		})
	}
	sort.Slice(sources, func(i, j int) bool {
		left := sources[i].Provider + "/" + sources[i].SourceID
		right := sources[j].Provider + "/" + sources[j].SourceID
		return left < right
	})
	return sources
}

func (r stockCNRoute) providersFor(feed, wantedState string) []string {
	providers := make([]string, 0, len(r.Providers))
	for _, provider := range r.Providers {
		state := provider.Kline
		if feed == "instrument" {
			state = provider.Instrument
		}
		if strings.EqualFold(strings.TrimSpace(state), wantedState) {
			providers = append(providers, strings.TrimSpace(provider.ID))
		}
	}
	return providers
}

func stockCNRoutePathCandidates() []string {
	candidates := []string{
		strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_ROUTE_PATH")),
		"markets/stock_cn/route.yaml",
		"config/markets/stock_cn/route.yaml",
		"modules/collector/config/markets/stock_cn/route.yaml",
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		collectorDir := filepath.Dir(sourceFile)
		candidates = append(candidates, filepath.Join(collectorDir, "..", "..", "config", "markets", "stock_cn", "route.yaml"))
	}
	return candidates
}

func loadStockCNRoute() (stockCNRoute, error) {
	for _, candidate := range stockCNRoutePathCandidates() {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Clean(candidate)); err != nil {
			continue
		}
		return loadStockCNRouteFile(candidate)
	}
	return stockCNRoute{}, fmt.Errorf("stock_cn route config was not found")
}

type stockCNProviderFile struct {
	Provider stockCNProviderBody `yaml:"provider"`
}

type stockCNProviderBody struct {
	ID           string                       `yaml:"id"`
	Market       string                       `yaml:"market"`
	EnabledFeeds []string                     `yaml:"enabled_feeds"`
	ShadowFeeds  []string                     `yaml:"shadow_feeds"`
	Hosts        []string                     `yaml:"hosts"`
	Kline        stockCNProviderKlineFile     `yaml:"kline"`
	RateLimit    stockCNProviderRateLimitFile `yaml:"rate_limit"`
}

type stockCNProviderKlineFile struct {
	Endpoint          string `yaml:"endpoint"`
	Frequency         string `yaml:"frequency"`
	Mode              string `yaml:"mode"`
	MaxBarsPerRequest int    `yaml:"max_bars_per_request"`
	VolumeUnit        string `yaml:"volume_unit"`
	AmountUnit        string `yaml:"amount_unit"`
	CompleteOHLCV     *bool  `yaml:"complete_ohlcv"`
}

type stockCNProviderRateLimitFile struct {
	Scope             string  `yaml:"scope"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
	MaxConcurrent     int     `yaml:"max_concurrent"`
	Cooldown          string  `yaml:"cooldown"`
	RequestTimeout    string  `yaml:"request_timeout"`
}

type stockCNProviderRuntime struct {
	ID                string
	KlineEnabled      bool
	InstrumentEnabled bool
	KlineShadow       bool
	KlineBaseURL      string
	KlineEndpoint     string
	Hosts             []string
	Port              int
	KlineSpec         stockCNProviderKlineFile
	RateLimit         marketdata.RateLimitPolicy
}

func loadStockCNProviderRuntime(route stockCNRoute) (map[string]stockCNProviderRuntime, error) {
	paths := stockCNProviderConfigPaths()
	result := make(map[string]stockCNProviderRuntime, len(route.Providers))
	for _, routeProvider := range route.Providers {
		id := strings.TrimSpace(routeProvider.ID)
		var file stockCNProviderFile
		var found string
		for _, dir := range paths {
			candidate := filepath.Join(dir, id+".yaml")
			if _, err := os.Stat(candidate); err != nil {
				continue
			}
			raw, err := os.ReadFile(candidate)
			if err != nil {
				return nil, err
			}
			decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
			decoder.KnownFields(true)
			if err := decoder.Decode(&file); err != nil {
				return nil, fmt.Errorf("decode provider config %s: %w", candidate, err)
			}
			found = candidate
			break
		}
		if found == "" {
			return nil, fmt.Errorf("stock_cn provider config %q was not found", id)
		}
		if err := validateStockCNProviderFile(id, file); err != nil {
			return nil, fmt.Errorf("validate provider config %s: %w", found, err)
		}
		baseURL, endpoint, err := splitStockCNEndpoint(file.Provider.Kline.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("provider %q kline.endpoint: %w", id, err)
		}
		rateLimit, err := file.Provider.RateLimit.Policy()
		if err != nil {
			return nil, fmt.Errorf("provider %q rate_limit: %w", id, err)
		}
		result[id] = stockCNProviderRuntime{
			ID:                id,
			KlineEnabled:      containsStringFold(file.Provider.EnabledFeeds, "kline"),
			InstrumentEnabled: containsStringFold(file.Provider.EnabledFeeds, "instrument"),
			KlineShadow:       containsStringFold(file.Provider.ShadowFeeds, "kline") || strings.EqualFold(file.Provider.Kline.Mode, "shadow"),
			KlineBaseURL:      baseURL,
			KlineEndpoint:     endpoint,
			Hosts:             append([]string(nil), file.Provider.Hosts...),
			Port:              providerPort(id),
			KlineSpec:         file.Provider.Kline,
			RateLimit:         rateLimit,
		}
	}
	return result, nil
}

func providerPort(providerID string) int {
	if strings.EqualFold(strings.TrimSpace(providerID), "tdx") {
		return 7709
	}
	return 443
}

func splitStockCNEndpoint(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if parsed.IsAbs() {
		if parsed.Host == "" || parsed.Path == "" {
			return "", "", fmt.Errorf("absolute endpoint must include host and path")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", fmt.Errorf("endpoint must not include query or fragment")
		}
		return strings.TrimRight(parsed.Scheme+"://"+parsed.Host, "/"), parsed.EscapedPath(), nil
	}
	if !strings.HasPrefix(parsed.Path, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("endpoint must be an absolute URL or path")
	}
	return "", parsed.EscapedPath(), nil
}

func validateStockCNProviderFile(routeID string, file stockCNProviderFile) error {
	provider := file.Provider
	if strings.TrimSpace(provider.ID) != routeID {
		return fmt.Errorf("provider.id %q does not match route id %q", provider.ID, routeID)
	}
	if strings.TrimSpace(provider.Market) != StockCNSpaceID || len(provider.Hosts) == 0 {
		return fmt.Errorf("market must be %q and hosts are required", StockCNSpaceID)
	}
	if provider.Kline.Frequency != "" && provider.Kline.Frequency != "1m" {
		return fmt.Errorf("only 1m kline is supported")
	}
	if provider.Kline.MaxBarsPerRequest < 0 {
		return fmt.Errorf("max_bars_per_request cannot be negative")
	}
	return nil
}

func (r stockCNProviderRateLimitFile) Policy() (marketdata.RateLimitPolicy, error) {
	cooldown, err := time.ParseDuration(strings.TrimSpace(r.Cooldown))
	if err != nil {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("parse cooldown: %w", err)
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(r.RequestTimeout))
	if err != nil {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("parse request_timeout: %w", err)
	}
	if strings.TrimSpace(r.Scope) != "egress_ip" {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("scope must be egress_ip")
	}
	policy := marketdata.RateLimitPolicy{RequestsPerSecond: r.RequestsPerSecond, Burst: r.Burst, MaxConcurrent: r.MaxConcurrent, Cooldown: cooldown, RequestTimeout: timeout}
	if err := policy.Validate(); err != nil {
		return marketdata.RateLimitPolicy{}, err
	}
	return policy, nil
}

func stockCNProviderConfigPaths() []string {
	paths := []string{
		strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_SOURCE_CONFIG_DIR")),
		"sources/market",
		"config/sources/market",
		"configs/scf/stock_cn/sources/market",
		"configs/sources/market",
		"modules/collector/configs/scf/stock_cn/sources/market",
		"modules/collector/configs/sources/market",
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		collectorDir := filepath.Dir(sourceFile)
		paths = append(paths, filepath.Join(collectorDir, "..", "..", "configs", "scf", "stock_cn", "sources", "market"))
	}
	return paths
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
