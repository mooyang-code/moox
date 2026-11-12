package marketfetch

import (
	"sync"

	"trpc.group/trpc-go/trpc-go/log"
)

// FleetSizing 是按配置估算“每个函数负责多少个标的”的参数。函数个数 = ceil(标的数 ÷ 每函数标的数)，
// 标的变多或限速配置变化时，函数个数随计划自动调整，不用手工改 symbols_per_function。
type FleetSizing struct {
	// TargetFetchMS 是希望一个函数取完自己那批标的的时间（毫秒）。
	TargetFetchMS int
	// RequestMS 是单个行情请求的典型耗时（毫秒）；TargetFetchMS ÷ RequestMS 是每个并发槽位在目标时间内能连续发出的请求数。
	RequestMS int
	// Workers 是一个函数内同时处理的标的数上限（计划里的 fetch.concurrency），不分数据源；0 表示不限制。
	Workers int
	// MinFunctions 是函数个数的下限：标的少时也至少分成这么多个，避免一个函数故障就丢掉大部分标的。
	MinFunctions int
}

// SourceCapacity 是一个主源在一个函数进程内的承受能力（取自该源的进程内限速配置）与分配权重。
type SourceCapacity struct {
	Provider      string
	Weight        int
	MaxConcurrent int
	RPS           float64
	Burst         int
}

// fleetSkewMargin 是按标的哈希分配时各源实际分到的数量相对期望值的波动余量（二项分布，每函数几十只时约 ±15%）。
const fleetSkewMargin = 1.2

// MaxAutoSymbolsPerFunction 是估算结果的上限，与配置里每函数标的数的上限一致。
const MaxAutoSymbolsPerFunction = 200

// SymbolsPerFunction 估算每个函数负责的标的数。
//
// 一个函数有 S 只标的时，源 i 分到约 S × w_i ÷ W 只（W 是主源权重之和）。它在目标时间 T 内最多能处理
// min(并发数 × (T ÷ 单请求耗时), 突发 + 每秒上限 × T) 个请求，所以 S ≤ 该容量 × W ÷ w_i ÷ 余量；
// 各源的最小值就是每函数标的数；函数内同时只处理 Workers 只，所以还要不超过 Workers × (T ÷ 单请求耗时)。
// 最后与“至少 MinFunctions 个函数”取较小值。算不出来（没有主源、配置缺失）返回 0，由调用方用默认值。
func (f FleetSizing) SymbolsPerFunction(sources []SourceCapacity, subjects int) int {
	if subjects <= 0 || f.TargetFetchMS <= 0 || f.RequestMS <= 0 {
		return 0
	}
	totalWeight := 0
	for _, source := range sources {
		if source.Weight > 0 {
			totalWeight += source.Weight
		}
	}
	if totalWeight == 0 {
		return 0
	}
	waves := max(1, f.TargetFetchMS/f.RequestMS)
	seconds := float64(f.TargetFetchMS) / 1000
	size := MaxAutoSymbolsPerFunction
	if f.Workers > 0 {
		size = min(size, f.Workers*waves)
	}
	for _, source := range sources {
		if source.Weight <= 0 {
			continue
		}
		capacity := float64(max(1, source.MaxConcurrent) * waves)
		if source.RPS > 0 {
			capacity = min(capacity, float64(source.Burst)+source.RPS*seconds)
		}
		size = min(size, int(capacity*float64(totalWeight)/float64(source.Weight)/fleetSkewMargin))
	}
	if f.MinFunctions > 1 {
		size = min(size, (subjects+f.MinFunctions-1)/f.MinFunctions)
	}
	return max(size, 1)
}

// routeCapacities 返回市场某个频率的各主源在一个函数进程内的承受能力：权重取自该频率的路由，并发与频率上限取自各源的限速配置。
// 市场没有路由文件、路由或源配置读不出来时返回 nil（调用方沿用配置里的默认值），不影响计划生成。
// 与市场里的品种无关：有路由文件的市场，全部分组都按路由的主源来算。
func routeCapacities(marketID string) func(frequency string) []SourceCapacity {
	var mu sync.Mutex
	var route Route
	var runtimes map[string]SourceRuntime
	return func(frequency string) []SourceCapacity {
		mu.Lock()
		defer mu.Unlock()
		if runtimes == nil {
			loaded, found, err := LoadRoute(marketID)
			if err != nil {
				log.Warnf("%s 函数容量估算：读路由失败：%v", marketID, err)
				return nil
			}
			if !found {
				return nil
			}
			sources, err := LoadSourceRuntimes(loaded)
			if err != nil {
				log.Warnf("%s 函数容量估算：读数据源配置失败：%v", marketID, err)
				return nil
			}
			route, runtimes = loaded, sources
		}
		kline, err := route.Kline(frequency)
		if err != nil {
			return nil
		}
		var sources []SourceCapacity
		for _, source := range kline.PrimarySources() {
			runtime, ok := runtimes[source.Provider]
			if !ok {
				return nil
			}
			sources = append(sources, SourceCapacity{
				Provider: source.Provider, Weight: source.Weight,
				MaxConcurrent: runtime.RateLimit.MaxConcurrent, RPS: runtime.RateLimit.RequestsPerSecond, Burst: runtime.RateLimit.Burst,
			})
		}
		return sources
	}
}

// routeWorkers 是一个函数内同时处理的标的数：各主源并发之和（各源在函数内的并发上限见源配置），不超过 MaxConcurrency。
func routeWorkers(sources []SourceCapacity) int {
	total := 0
	for _, source := range sources {
		total += max(1, source.MaxConcurrent)
	}
	return min(total, MaxConcurrency)
}

// RouteAutoSize 返回按市场的路由估算每函数标的数的函数（见 FleetSizing）；函数内同时处理的标的数取各主源并发之和。
// 市场没有路由文件、或没有这个频率的路由时估算结果为 0，由调用方用默认值。
func RouteAutoSize(marketID string, sizing FleetSizing) func(sample TaskGroup, subjects int) int {
	capacities := routeCapacities(marketID)
	return func(sample TaskGroup, subjects int) int {
		sources := capacities(sample.Frequency)
		if len(sources) == 0 {
			return 0
		}
		if sizing.Workers == 0 {
			sizing.Workers = routeWorkers(sources)
		}
		return sizing.SymbolsPerFunction(sources, subjects)
	}
}

// RouteFetchLimits 返回按市场的路由推导计划里 fetch 参数的函数：函数内并发（concurrency）取各主源并发之和，
// 熔断用的并发（feed_concurrency）取各主源并发的最大值；每秒请求数与突发（rps、burst）是随请求下发给单一来源数据源的限速，
// 按路由分配的来源各自用源配置里的限速，所以清零。市场没有路由文件、没有这个频率的路由、读不出源配置时保持传入的值。
func RouteFetchLimits(marketID string) func(sample TaskGroup, fetch PlanFetch) PlanFetch {
	capacities := routeCapacities(marketID)
	return func(sample TaskGroup, fetch PlanFetch) PlanFetch {
		sources := capacities(sample.Frequency)
		if len(sources) == 0 {
			return fetch
		}
		feed := 1
		for _, source := range sources {
			feed = max(feed, source.MaxConcurrent)
		}
		fetch.Concurrency, fetch.FeedConcurrency, fetch.RPS, fetch.Burst = routeWorkers(sources), feed, 0, 0
		return fetch
	}
}
