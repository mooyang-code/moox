package marketfetch

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

// stockCNRoute 是 A 股的路由配置（config/markets/stockcn/route.yaml，编在二进制里）：标的快照来源，以及按频率分开的 K 线候选链。
// 由 SCF 组合根加载，数据源代码里不重复登记。
type stockCNRoute struct {
	MarketID string `yaml:"market_id"`
	// InstrumentProviderIDs 是完整标的快照的来源，多家并发取数后按 SubjectID 合并。
	InstrumentProviderIDs []string            `yaml:"instrument_providers"`
	Klines                []stockCNKlineRoute `yaml:"klines"`
}

// stockCNKlineRoute 是一个频率的 K 线候选链：primary 参与按标的分配，fallback 只在前面的来源失败后使用。
type stockCNKlineRoute struct {
	Frequency string                 `yaml:"frequency"`
	Providers []stockCNRouteProvider `yaml:"providers"`
}

type stockCNRouteProvider struct {
	ID       string `yaml:"id"`
	SourceID string `yaml:"source_id"`
	Weight   int    `yaml:"weight"`
	Role     string `yaml:"role"`
}

// StockCNRouteID 返回 A 股某个频率的 K 线路由 ID：写入行的 route_id，也是按标的分配来源时的哈希种子。
func StockCNRouteID(frequency string) string {
	if parsed, err := marketdata.ParseFrequency(frequency); err == nil {
		frequency = string(parsed)
	}
	return stockCNKlineRoutePrefix + strings.ToLower(strings.TrimSpace(frequency))
}

const stockCNKlineRoutePrefix = "stockcn_equity_kline_"

func loadStockCNRouteFile(path string) (stockCNRoute, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stockCNRoute{}, err
	}
	return parseStockCNRoute(raw)
}

func parseStockCNRoute(raw []byte) (stockCNRoute, error) {
	var route stockCNRoute
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&route); err != nil {
		return stockCNRoute{}, err
	}
	if err := route.Validate(); err != nil {
		return stockCNRoute{}, fmt.Errorf("校验 stockcn 路由：%w", err)
	}
	return route, nil
}

func (r stockCNRoute) Validate() error {
	if strings.TrimSpace(r.MarketID) != StockCNSpaceID {
		return fmt.Errorf("market_id 必须是 %q", StockCNSpaceID)
	}
	if len(r.Klines) == 0 {
		return fmt.Errorf("至少要配置一个频率的 K 线路由")
	}
	frequencies := make(map[string]struct{}, len(r.Klines))
	for _, kline := range r.Klines {
		parsed, err := marketdata.ParseFrequency(kline.Frequency)
		if err != nil || string(parsed) != strings.TrimSpace(kline.Frequency) {
			return fmt.Errorf("K 线路由的频率 %q 无效", kline.Frequency)
		}
		if _, ok := frequencies[string(parsed)]; ok {
			return fmt.Errorf("频率 %s 的 K 线路由重复", parsed)
		}
		frequencies[string(parsed)] = struct{}{}
		seen := make(map[string]struct{}, len(kline.Providers))
		for _, provider := range kline.Providers {
			id := strings.TrimSpace(provider.ID)
			if id == "" {
				return fmt.Errorf("频率 %s：provider id 不能为空", parsed)
			}
			if _, ok := seen[id]; ok {
				return fmt.Errorf("频率 %s：provider %q 重复", parsed, id)
			}
			seen[id] = struct{}{}
			if provider.Weight <= 0 {
				return fmt.Errorf("频率 %s：provider %q 的 weight 必须为正", parsed, id)
			}
			switch strings.ToLower(strings.TrimSpace(provider.Role)) {
			case "primary", "fallback":
			default:
				return fmt.Errorf("频率 %s：provider %q 的 role 必须是 primary 或 fallback", parsed, id)
			}
		}
		if len(kline.PrimarySources()) == 0 {
			return fmt.Errorf("频率 %s 至少要有一个 primary 来源", parsed)
		}
	}
	return nil
}

// Kline 返回某个频率的 K 线路由；没有配置该频率时报错。
func (r stockCNRoute) Kline(frequency string) (stockCNKlineRoute, error) {
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return stockCNKlineRoute{}, err
	}
	for _, kline := range r.Klines {
		if strings.TrimSpace(kline.Frequency) == string(parsed) {
			return kline, nil
		}
	}
	return stockCNKlineRoute{}, fmt.Errorf("stockcn 没有 %s 的 K 线路由", parsed)
}

// InstrumentProviders 返回标的快照的来源。
func (r stockCNRoute) InstrumentProviders() []string {
	out := make([]string, 0, len(r.InstrumentProviderIDs))
	for _, id := range r.InstrumentProviderIDs {
		out = append(out, strings.ToLower(strings.TrimSpace(id)))
	}
	return out
}

// ProviderIDs 返回路由里出现的全部数据源（标的来源与各频率的 K 线来源），按首次出现的顺序。
func (r stockCNRoute) ProviderIDs() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		if _, ok := seen[id]; !ok && id != "" {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, id := range r.InstrumentProviderIDs {
		add(id)
	}
	for _, kline := range r.Klines {
		for _, provider := range kline.Providers {
			add(provider.ID)
		}
	}
	return out
}

// RouteID 返回该频率的路由 ID。
func (k stockCNKlineRoute) RouteID() string { return StockCNRouteID(k.Frequency) }

// ProviderChain 返回候选链：按配置顺序，primary 在前、fallback 在后由配置保证。
func (k stockCNKlineRoute) ProviderChain() []string {
	out := make([]string, 0, len(k.Providers))
	for _, provider := range k.Providers {
		out = append(out, strings.ToLower(strings.TrimSpace(provider.ID)))
	}
	return out
}

type stockCNSource struct {
	Provider string
	SourceID string
	Weight   int
}

// Sources 返回候选链上全部来源。
func (k stockCNKlineRoute) Sources() []stockCNSource { return k.sources(false) }

// PrimarySources 返回参与按标的分配的来源。
func (k stockCNKlineRoute) PrimarySources() []stockCNSource { return k.sources(true) }

func (k stockCNKlineRoute) sources(primaryOnly bool) []stockCNSource {
	sources := make([]stockCNSource, 0, len(k.Providers))
	for _, provider := range k.Providers {
		if primaryOnly && !strings.EqualFold(strings.TrimSpace(provider.Role), "primary") {
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

// readStockCNMarketFile 读 A 股的一个市场数据文件：环境变量指定的 override 路径存在就读它，
// 否则（没设，或目录、文件不存在）读编进二进制的内容。设了却没有这个文件时打一条警告，每个路径只打一次。
func readStockCNMarketFile(override, embedded string) ([]byte, error) {
	if override != "" {
		raw, err := os.ReadFile(override)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return raw, err
		}
		if _, warned := missingOverrides.LoadOrStore(override, struct{}{}); !warned {
			log.Warnf("环境变量指定的市场数据文件 %s 不存在，改用编进二进制的 stockcn/%s", override, embedded)
		}
	}
	return markets.ReadFile(StockCNSpaceID, embedded)
}

var missingOverrides sync.Map

// loadStockCNRoute 读 A 股的路由：环境变量 MOOX_STOCK_CN_ROUTE_PATH 指定的文件存在就读它，否则读编进二进制的 route.yaml。
func loadStockCNRoute() (stockCNRoute, error) {
	raw, err := readStockCNMarketFile(strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_ROUTE_PATH")), "route.yaml")
	if err != nil {
		return stockCNRoute{}, fmt.Errorf("读取 stockcn 路由：%w", err)
	}
	return parseStockCNRoute(raw)
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
	ids := route.ProviderIDs()
	result := make(map[string]stockCNProviderRuntime, len(ids))
	for _, id := range ids {
		source, found, err := loadStockCNSourceRuntime(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("stockcn provider config %q was not found", id)
		}
		result[id] = source
	}
	return result, nil
}

// loadStockCNSourceRuntime 按提供方 ID 读 A 股数据源的运行参数（sources/<id>.yaml）：路由里的 K 线来源和
// 只提供附加字段的来源都用它。环境变量 MOOX_STOCK_CN_SOURCE_CONFIG_DIR 指定的目录里有这个文件就读它，
// 否则读编进二进制的内容；两处都没有时 found 为 false。
func loadStockCNSourceRuntime(id string) (stockCNProviderRuntime, bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	path := "stockcn/sources/" + id + ".yaml"
	override := ""
	if dir := strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_SOURCE_CONFIG_DIR")); dir != "" {
		override = filepath.Join(dir, id+".yaml")
	}
	raw, err := readStockCNMarketFile(override, "sources/"+id+".yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return stockCNProviderRuntime{}, false, nil
	}
	if err != nil {
		return stockCNProviderRuntime{}, false, err
	}
	var file stockCNProviderFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return stockCNProviderRuntime{}, false, fmt.Errorf("decode provider config %s: %w", path, err)
	}
	if err := validateStockCNProviderFile(id, file); err != nil {
		return stockCNProviderRuntime{}, false, fmt.Errorf("validate provider config %s: %w", path, err)
	}
	baseURL, endpoint, err := splitStockCNEndpoint(file.Provider.Kline.Endpoint)
	if err != nil {
		return stockCNProviderRuntime{}, false, fmt.Errorf("provider %q kline.endpoint: %w", id, err)
	}
	rateLimit, err := file.Provider.RateLimit.Policy()
	if err != nil {
		return stockCNProviderRuntime{}, false, fmt.Errorf("provider %q rate_limit: %w", id, err)
	}
	return stockCNProviderRuntime{
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
	}, true, nil
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

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// stockCNAssignmentRoute 返回某个频率按标的分配来源用的路由 ID 与 primary 来源。
func stockCNAssignmentRoute(frequency string) (string, []stockCNSource, error) {
	route, err := loadStockCNRoute()
	if err != nil {
		return "", nil, fmt.Errorf("加载 stockcn 路由：%w", err)
	}
	kline, err := route.Kline(frequency)
	if err != nil {
		return "", nil, err
	}
	return kline.RouteID(), kline.PrimarySources(), nil
}

func weightedSourceBucket(routeVersion, subject string, sources []stockCNSource, totalWeight int) stockCNSource {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(routeVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(subject))
	bucket := int(hash.Sum64() % uint64(totalWeight))
	for _, source := range sources {
		if bucket < source.Weight {
			return source
		}
		bucket -= source.Weight
	}
	return sources[len(sources)-1]
}
