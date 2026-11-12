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

// MarketConfigDirEnv 指定一个目录来覆盖编进二进制的市场数据文件，布局与 config/markets 相同：
// <目录>/<市场>/route.yaml、<目录>/<市场>/sources/<数据源>.yaml。目录里没有的文件仍读编进去的内容。
const MarketConfigDirEnv = "MOOX_MARKET_CONFIG_DIR"

// Route 是一个市场的路由文件（config/markets/<市场>/route.yaml，编在二进制里）：一个品种的 K 线来源按它分配——
// 每个频率一条候选链，主源按权重、按标的哈希分配，失败沿链回落。没有路由文件的市场、路由没管的品种，
// K 线来源是任务或标签指定的那一个。由组合根加载，数据源代码里不重复登记。
type Route struct {
	MarketID string `yaml:"market_id"`
	// InstrumentType 是这份路由管的品种；行情源目录里这个（市场，品种）的 K 线数据源登记为按路由分配。
	InstrumentType string `yaml:"instrument_type"`
	// SettleDelayMS 是结算延迟（毫秒）：实时采集只写“收盘时刻 + 结算延迟”已经过去的 K 线，数据源在这之前给的可能是未定稿值。
	// 采集计划的 not_before_ms 不能小于它，否则目标那根每期都被丢掉。
	SettleDelayMS int `yaml:"settle_delay_ms"`
	// InstrumentProviderIDs 是完整标的快照的来源，多家并发取数后按 SubjectID 合并。
	InstrumentProviderIDs []string     `yaml:"instrument_providers"`
	Klines                []KlineRoute `yaml:"klines"`
}

// KlineRoute 是一个频率的 K 线候选链：primary 参与按标的分配，fallback 只在前面的来源失败后使用。
type KlineRoute struct {
	Frequency string          `yaml:"frequency"`
	Providers []RouteProvider `yaml:"providers"`
	// routeID 在从 Route 取出时填上（见 Route.Kline）。
	routeID string
}

// RouteProvider 是候选链上的一个来源。
type RouteProvider struct {
	ID       string `yaml:"id"`
	SourceID string `yaml:"source_id"`
	Weight   int    `yaml:"weight"`
	Role     string `yaml:"role"`
}

// RouteSource 是候选链上一个来源的分配信息。
type RouteSource struct {
	Provider string
	SourceID string
	Weight   int
}

// KlineRouteID 返回按路由分配来源的（市场，品种）某个频率的路由 ID：<市场>_<品种>_kline_<频率>。
// 它写入行的 route_id，也是按标的分配来源时的哈希种子，取值不能变（变了所有标的会重新分配）。
func KlineRouteID(marketID string, instrumentType marketdata.InstrumentType, frequency string) string {
	if parsed, err := marketdata.ParseFrequency(frequency); err == nil {
		frequency = string(parsed)
	}
	return normalizeMarketID(marketID) + "_" + string(instrumentType) + "_kline_" + strings.ToLower(strings.TrimSpace(frequency))
}

func normalizeMarketID(marketID string) string { return strings.ToLower(strings.TrimSpace(marketID)) }

func parseRoute(marketID string, raw []byte) (Route, error) {
	var route Route
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&route); err != nil {
		return Route{}, err
	}
	if err := route.validate(marketID); err != nil {
		return Route{}, fmt.Errorf("校验 %s 路由：%w", marketID, err)
	}
	return route, nil
}

// validate 校验路由；marketID 是路由文件所在目录的市场。
func (r Route) validate(marketID string) error {
	if strings.TrimSpace(r.MarketID) != marketID {
		return fmt.Errorf("market_id 必须是 %q", marketID)
	}
	if strings.TrimSpace(r.InstrumentType) == "" || strings.TrimSpace(r.InstrumentType) != string(r.Instrument()) {
		return fmt.Errorf("instrument_type 必填，写小写的品种名")
	}
	if r.SettleDelayMS < 0 {
		return fmt.Errorf("settle_delay_ms 不能为负")
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

// Instrument 返回这份路由管的品种。
func (r Route) Instrument() marketdata.InstrumentType {
	return marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(r.InstrumentType)))
}

// SettleDelay 返回结算延迟。
func (r Route) SettleDelay() time.Duration { return time.Duration(r.SettleDelayMS) * time.Millisecond }

// Kline 返回某个频率的 K 线路由；没有配置该频率时报错。
func (r Route) Kline(frequency string) (KlineRoute, error) {
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return KlineRoute{}, err
	}
	for _, kline := range r.Klines {
		if strings.TrimSpace(kline.Frequency) == string(parsed) {
			kline.routeID = KlineRouteID(r.MarketID, r.Instrument(), kline.Frequency)
			return kline, nil
		}
	}
	return KlineRoute{}, fmt.Errorf("%s 没有 %s 的 K 线路由", strings.TrimSpace(r.MarketID), parsed)
}

// InstrumentProviders 返回标的快照的来源。
func (r Route) InstrumentProviders() []string {
	out := make([]string, 0, len(r.InstrumentProviderIDs))
	for _, id := range r.InstrumentProviderIDs {
		out = append(out, strings.ToLower(strings.TrimSpace(id)))
	}
	return out
}

// ProviderIDs 返回路由里出现的全部数据源（标的来源与各频率的 K 线来源），按首次出现的顺序。
func (r Route) ProviderIDs() []string {
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
func (k KlineRoute) RouteID() string { return k.routeID }

// ProviderChain 返回候选链：按配置顺序，primary 在前、fallback 在后由配置保证。
func (k KlineRoute) ProviderChain() []string {
	out := make([]string, 0, len(k.Providers))
	for _, provider := range k.Providers {
		out = append(out, strings.ToLower(strings.TrimSpace(provider.ID)))
	}
	return out
}

// Sources 返回候选链上全部来源。
func (k KlineRoute) Sources() []RouteSource { return k.sources(false) }

// PrimarySources 返回参与按标的分配的来源。
func (k KlineRoute) PrimarySources() []RouteSource { return k.sources(true) }

func (k KlineRoute) sources(primaryOnly bool) []RouteSource {
	sources := make([]RouteSource, 0, len(k.Providers))
	for _, provider := range k.Providers {
		if primaryOnly && !strings.EqualFold(strings.TrimSpace(provider.Role), "primary") {
			continue
		}
		sourceID := strings.TrimSpace(provider.SourceID)
		if sourceID == "" {
			sourceID = strings.TrimSpace(provider.ID) + "_http"
		}
		sources = append(sources, RouteSource{
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

// readMarketFile 读一个市场的数据文件（name 形如 route.yaml、sources/tdx.yaml）：环境变量 MOOX_MARKET_CONFIG_DIR
// 指定的目录里有这个文件就读它，否则读编进二进制的内容；两处都没有时返回 fs.ErrNotExist。
// 设了目录却没有这个文件、而编进去的内容里有时打一条警告，每个路径只打一次。
func readMarketFile(marketID, name string) ([]byte, error) {
	override := ""
	if dir := strings.TrimSpace(os.Getenv(MarketConfigDirEnv)); dir != "" {
		override = filepath.Join(dir, marketID, filepath.FromSlash(name))
		raw, err := os.ReadFile(override)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return raw, err
		}
	}
	raw, err := markets.ReadFile(marketID, name)
	if err == nil && override != "" {
		if _, warned := missingOverrides.LoadOrStore(override, struct{}{}); !warned {
			log.Warnf("环境变量 %s 指定的目录里没有 %s，改用编进二进制的 %s/%s", MarketConfigDirEnv, override, marketID, name)
		}
	}
	return raw, err
}

var missingOverrides sync.Map

// LoadRoute 读市场的路由文件；市场没有路由文件时 found 为 false。
func LoadRoute(marketID string) (route Route, found bool, err error) {
	marketID = normalizeMarketID(marketID)
	raw, err := readMarketFile(marketID, "route.yaml")
	if errors.Is(err, fs.ErrNotExist) || marketID == "" {
		return Route{}, false, nil
	}
	if err != nil {
		return Route{}, false, fmt.Errorf("读取 %s 路由：%w", marketID, err)
	}
	route, err = parseRoute(marketID, raw)
	return route, err == nil, err
}

// RouteFor 返回管（市场，品种）K 线来源的路由；这个品种的来源不按路由分配（市场没有路由文件，或路由管的是别的品种）时
// found 为 false。instrumentType 为空时按市场的默认品种。
func RouteFor(marketID string, instrumentType marketdata.InstrumentType) (route Route, found bool, err error) {
	route, found, err = LoadRoute(marketID)
	if err != nil || !found {
		return Route{}, false, err
	}
	if instrumentType == "" {
		if instrumentType, err = defaultInstrumentType(marketID, ""); err != nil {
			return Route{}, false, err
		}
	}
	if route.Instrument() != instrumentType {
		return Route{}, false, nil
	}
	return route, true, nil
}

// routeAssignment 返回（市场，品种）某个频率按标的分配来源用的路由 ID 与 primary 来源。
func routeAssignment(marketID string, instrumentType marketdata.InstrumentType, frequency string) (string, []RouteSource, error) {
	route, found, err := RouteFor(marketID, instrumentType)
	if err != nil {
		return "", nil, fmt.Errorf("加载 %s 路由：%w", marketID, err)
	}
	if !found {
		return "", nil, fmt.Errorf("%s 的 %s 没有路由文件，不能按标的分配来源", marketID, instrumentType)
	}
	kline, err := route.Kline(frequency)
	if err != nil {
		return "", nil, err
	}
	return kline.RouteID(), kline.PrimarySources(), nil
}

// assignedSource 返回标的在 primary 来源之间按权重、按（路由 ID，标的）哈希分到的那一个。
func assignedSource(sources []RouteSource, routeID, subject string) (RouteSource, bool) {
	totalWeight := 0
	for _, source := range sources {
		totalWeight += source.Weight
	}
	if totalWeight <= 0 || len(sources) == 0 {
		return RouteSource{}, false
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(routeID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(subject))
	bucket := int(hash.Sum64() % uint64(totalWeight))
	selected := sources[len(sources)-1]
	for _, source := range sources {
		if bucket < source.Weight {
			selected = source
			break
		}
		bucket -= source.Weight
	}
	return selected, selected.Provider != "" && selected.SourceID != ""
}

type sourceFile struct {
	Provider sourceFileBody `yaml:"provider"`
}

type sourceFileBody struct {
	ID           string              `yaml:"id"`
	Market       string              `yaml:"market"`
	EnabledFeeds []string            `yaml:"enabled_feeds"`
	ShadowFeeds  []string            `yaml:"shadow_feeds"`
	Hosts        []string            `yaml:"hosts"`
	Kline        SourceKlineConfig   `yaml:"kline"`
	RateLimit    sourceRateLimitFile `yaml:"rate_limit"`
}

// SourceKlineConfig 是数据源配置文件里的 K 线一段。
type SourceKlineConfig struct {
	Endpoint          string `yaml:"endpoint"`
	Mode              string `yaml:"mode"`
	MaxBarsPerRequest int    `yaml:"max_bars_per_request"`
	VolumeUnit        string `yaml:"volume_unit"`
	AmountUnit        string `yaml:"amount_unit"`
	CompleteOHLCV     *bool  `yaml:"complete_ohlcv"`
}

type sourceRateLimitFile struct {
	Scope             string  `yaml:"scope"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
	MaxConcurrent     int     `yaml:"max_concurrent"`
	Cooldown          string  `yaml:"cooldown"`
	RequestTimeout    string  `yaml:"request_timeout"`
}

// SourceRuntime 是一个数据源的运行参数（config/markets/<市场>/sources/<数据源>.yaml）。
type SourceRuntime struct {
	ID                string
	KlineEnabled      bool
	InstrumentEnabled bool
	KlineShadow       bool
	KlineBaseURL      string
	KlineEndpoint     string
	Hosts             []string
	Port              int
	KlineSpec         SourceKlineConfig
	RateLimit         marketdata.RateLimitPolicy
}

// LoadSourceRuntimes 读路由里出现的全部数据源的运行参数，缺一个就报错。
func LoadSourceRuntimes(route Route) (map[string]SourceRuntime, error) {
	ids := route.ProviderIDs()
	result := make(map[string]SourceRuntime, len(ids))
	for _, id := range ids {
		source, found, err := LoadSourceRuntime(route.MarketID, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%s 的数据源 %q 没有配置文件", route.MarketID, id)
		}
		result[id] = source
	}
	return result, nil
}

// LoadSourceRuntime 按数据源 ID 读某个市场数据源的运行参数：路由里的 K 线来源和只提供附加字段的来源都用它。
// 没有配置文件时 found 为 false。
func LoadSourceRuntime(marketID, id string) (SourceRuntime, bool, error) {
	marketID = normalizeMarketID(marketID)
	id = strings.ToLower(strings.TrimSpace(id))
	name := "sources/" + id + ".yaml"
	path := marketID + "/" + name
	raw, err := readMarketFile(marketID, name)
	if errors.Is(err, fs.ErrNotExist) || marketID == "" || id == "" {
		return SourceRuntime{}, false, nil
	}
	if err != nil {
		return SourceRuntime{}, false, err
	}
	var file sourceFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return SourceRuntime{}, false, fmt.Errorf("解析数据源配置 %s：%w", path, err)
	}
	if err := validateSourceFile(marketID, id, file); err != nil {
		return SourceRuntime{}, false, fmt.Errorf("校验数据源配置 %s：%w", path, err)
	}
	baseURL, endpoint, err := splitEndpoint(file.Provider.Kline.Endpoint)
	if err != nil {
		return SourceRuntime{}, false, fmt.Errorf("数据源 %q 的 kline.endpoint：%w", id, err)
	}
	rateLimit, err := file.Provider.RateLimit.Policy()
	if err != nil {
		return SourceRuntime{}, false, fmt.Errorf("数据源 %q 的 rate_limit：%w", id, err)
	}
	return SourceRuntime{
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

func splitEndpoint(raw string) (string, string, error) {
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
			return "", "", fmt.Errorf("绝对地址必须带主机和路径")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", fmt.Errorf("地址不能带查询参数或片段")
		}
		return strings.TrimRight(parsed.Scheme+"://"+parsed.Host, "/"), parsed.EscapedPath(), nil
	}
	if !strings.HasPrefix(parsed.Path, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("地址必须是绝对 URL 或以 / 开头的路径")
	}
	return "", parsed.EscapedPath(), nil
}

// validateSourceFile 校验数据源配置；marketID 是文件所在目录的市场，id 是文件名里的数据源 ID。
func validateSourceFile(marketID, id string, file sourceFile) error {
	provider := file.Provider
	if strings.TrimSpace(provider.ID) != id {
		return fmt.Errorf("provider.id %q 与文件名里的数据源 %q 不一致", provider.ID, id)
	}
	if strings.TrimSpace(provider.Market) != marketID || len(provider.Hosts) == 0 {
		return fmt.Errorf("market 必须是 %q，hosts 必填", marketID)
	}
	if provider.Kline.MaxBarsPerRequest < 0 {
		return fmt.Errorf("max_bars_per_request 不能为负")
	}
	return nil
}

func (r sourceRateLimitFile) Policy() (marketdata.RateLimitPolicy, error) {
	cooldown, err := time.ParseDuration(strings.TrimSpace(r.Cooldown))
	if err != nil {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("解析 cooldown：%w", err)
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(r.RequestTimeout))
	if err != nil {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("解析 request_timeout：%w", err)
	}
	if strings.TrimSpace(r.Scope) != "egress_ip" {
		return marketdata.RateLimitPolicy{}, fmt.Errorf("scope 必须是 egress_ip")
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
