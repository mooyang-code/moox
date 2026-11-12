package marketwiring

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

// NewKlinePipeline 装一条 K 线流水线，全部市场、全部品种都走这里。走哪条装配路径看行情源目录：
// 这个（市场，品种）的数据源登记为按路由分配的，按 frequency 取路由文件里的候选链；否则用 providerID 指定的那一个数据源。
// 两条路径的差别（数据源怎么定、限速与熔断阈值从哪来、结算延迟、HTTP 客户端）都在各自的函数里，主流程不判断市场。
func NewKlinePipeline(storage marketfetch.Storage, marketID string, instrumentType marketdata.InstrumentType, frequency, providerID string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("K 线流水线缺少 Storage")
	}
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	var pipeline *marketfetch.KlinePipeline
	var err error
	if catalog.Default().Routed(marketdata.MarketID(marketID), instrumentType) {
		pipeline, err = newRoutedKlinePipeline(storage, marketID, instrumentType, frequency)
	} else {
		pipeline, err = newSingleSourceKlinePipeline(storage, marketID, instrumentType, providerID, limits)
	}
	if err != nil {
		return nil, err
	}
	if err := bindFields(pipeline); err != nil {
		return nil, err
	}
	return pipeline, nil
}

// newRoutedKlinePipeline 装按路由分配来源的流水线：候选链取路由里该频率的配置，来源按标的在 primary 之间分配，
// 失败后按候选链依次兜底。数据源的限速等运行参数来自各自的数据源配置文件，熔断阈值是候选链长度 × 每源尝试次数，
// 结算延迟取路由文件；不传 HTTP 客户端，各数据源用自己的。
func newRoutedKlinePipeline(storage marketfetch.Storage, marketID string, instrumentType marketdata.InstrumentType, frequency string) (*marketfetch.KlinePipeline, error) {
	route, found, err := marketfetch.RouteFor(marketID, instrumentType)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s 的 %s 数据源登记为按路由分配，但路由文件里没有这个品种", marketID, instrumentType)
	}
	kline, err := route.Kline(frequency)
	if err != nil {
		return nil, err
	}
	runtimes, err := marketfetch.LoadSourceRuntimes(route)
	if err != nil {
		return nil, err
	}
	chain := kline.ProviderChain()
	registry := marketdata.NewRegistry()
	for _, source := range kline.Sources() {
		runtime, ok := runtimes[source.Provider]
		if !ok || !runtime.KlineEnabled || runtime.KlineShadow {
			return nil, fmt.Errorf("%s K 线来源 %q 在数据源配置里没有启用", marketID, source.Provider)
		}
		provider, providerErr := newRoutedProvider(marketID, instrumentType, source.Provider, source.SourceID, runtime)
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
		SpaceID: marketID, MarketID: marketID, InstrumentType: instrumentType,
		SettleDelay: route.SettleDelay(),
	}, nil
}

// newRoutedProvider 从行情源目录构造按路由分配的数据源；sourceID 为空时取登记的默认值，运行参数来自数据源配置文件。
func newRoutedProvider(marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string, runtime marketfetch.SourceRuntime) (marketdata.MarketProvider, error) {
	entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), instrumentType, providerID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("行情源目录里没有 %s 的 %s 数据源 %q", marketID, instrumentType, providerID)
	}
	return entry.Build(marketdata.Deps{SourceID: sourceID, Settings: sourceSettings(runtime)})
}

// sourceSettings 把数据源配置文件的运行参数交给目录里的工厂。
func sourceSettings(runtime marketfetch.SourceRuntime) marketdata.SourceSettings {
	return marketdata.SourceSettings{
		BaseURL: runtime.KlineBaseURL, KlineEndpoint: runtime.KlineEndpoint, Hosts: runtime.Hosts, Port: runtime.Port,
		MaxBarsPerRequest: runtime.KlineSpec.MaxBarsPerRequest, RateLimit: runtime.RateLimit,
	}
}

// newSingleSourceKlinePipeline 装单一来源的流水线：数据源是任务或标签指定的那一个，source_id 取行情源目录里
// （市场，品种，数据源）登记的；与频率无关，没有结算延迟，HTTP 客户端用进程共享的。limits 非空时按它设置数据源限流与
// 熔断阈值（随请求给出）；为空时数据源用自己的默认值，熔断阈值按进程环境。
func newSingleSourceKlinePipeline(storage marketfetch.Storage, marketID string, instrumentType marketdata.InstrumentType, providerID string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), instrumentType, providerID, "")
	if err != nil || marketID == "" || providerID == "" {
		return nil, fmt.Errorf("行情源目录里没有 %s 的 %s 数据源 %q", marketID, instrumentType, providerID)
	}
	sourceID := entry.Key.SourceID
	provider, err := entry.Build(marketdata.Deps{HTTPClient: httpclient.SharedClientIfEnabled(), SourceID: sourceID, Settings: marketdata.SourceSettings{RateLimit: feedRateLimit(limits)}})
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, err
	}
	threshold := marketfetch.RuntimeKlineBreakerThreshold()
	if limits != nil && limits.FeedConcurrency > 0 {
		threshold = marketfetch.RuntimeKlineProviderAttemptBudget() * limits.FeedConcurrency
	}
	router, err := marketdata.NewRouter(registry, threshold, nil, nil)
	if err != nil {
		return nil, err
	}
	// 每个采集任务写自己的单频率数据集，流水线不绑定数据集，取请求里的。
	return &marketfetch.KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{providerID},
		RouteID: marketID + "_" + string(instrumentType) + "_kline",
		SpaceID: marketID, MarketID: marketID, InstrumentType: instrumentType,
		SourceID: sourceID,
	}, nil
}

// feedRateLimit 把计划的限流与超时转成数据源限流参数；零值字段由数据源按进程环境与默认取值。
func feedRateLimit(limits *marketfetch.FeedLimits) marketdata.RateLimitPolicy {
	if limits == nil {
		return marketdata.RateLimitPolicy{}
	}
	return marketdata.RateLimitPolicy{
		RequestsPerSecond: float64(limits.RPS), Burst: limits.Burst, MaxConcurrent: limits.FeedConcurrency,
		RequestTimeout: time.Duration(limits.RequestTimeoutMS) * time.Millisecond,
	}
}
