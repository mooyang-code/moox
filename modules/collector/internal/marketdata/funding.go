package marketdata

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// FundingRate 是一条永续合约的资金费结算记录。
type FundingRate struct {
	// ProviderSymbol 是交易所合约代码（如 BTCUSDT）。
	ProviderSymbol string
	// FundingTime 是结算时刻，保留交易所给出的毫秒精度（结算可能比整点晚 1 毫秒）。
	FundingTime time.Time
	// Rate 是交易所返回的小数原值，0.0001 即 0.01%。
	Rate float64
}

// FundingRateSource 是能查永续合约历史资金费率的行情源才实现的可选能力（公共接口，不需要密钥）。
// 采集管线与推送只依赖这个接口，不引用具体交易所。
type FundingRateSource interface {
	// FundingRates 返回结算时刻落在 [start, end) 内的记录，不保证顺序；分页由实现负责，返回完整结果。
	// symbol 为空时返回全部合约。
	FundingRates(ctx context.Context, symbol string, start, end time.Time) ([]FundingRate, error)
}

type realtimeFundingKey struct{}

// WithRealtimeFunding 标记这次资金费查询来自实时链路：限速时排在补采请求前面。
func WithRealtimeFunding(ctx context.Context) context.Context {
	return context.WithValue(ctx, realtimeFundingKey{}, true)
}

// IsRealtimeFunding 判断资金费查询是否来自实时链路。
func IsRealtimeFunding(ctx context.Context) bool {
	realtime, _ := ctx.Value(realtimeFundingKey{}).(bool)
	return realtime
}

// SumFundingRates 按合约汇总结算时刻落在 [start, end) 内的费率；没有结算的合约不出现在结果里（调用方按 0 处理）。
func SumFundingRates(rates []FundingRate, start, end time.Time) map[string]float64 {
	sums := make(map[string]float64)
	for _, rate := range rates {
		if rate.FundingTime.Before(start) || !rate.FundingTime.Before(end) {
			continue
		}
		sums[rate.ProviderSymbol] += rate.Rate
	}
	return sums
}

// 资金费取数失败的原因（指标标签）。
const (
	FundingFailureRateLimited = "rate_limited"
	FundingFailureHTTP        = "http_status"
	FundingFailureNetwork     = "network"
	FundingFailureProtocol    = "protocol"
	FundingFailureMalformed   = "malformed_record"
	FundingFailureCanceled    = "canceled"
)

var (
	fundingFailuresOnce sync.Once
	fundingFailures     = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_collector_funding_fetch_failures_total",
		Help: "资金费率取数失败次数（按原因）；全市场查询里被跳过的格式不对的记录也计入。",
	}, []string{"reason"})
)

// ObserveFundingFetchFailure 记一次资金费取数失败。计数器是进程级单例，第一次使用时注册到默认注册表。
func ObserveFundingFetchFailure(reason string) {
	fundingFailuresOnce.Do(func() { _ = prometheus.DefaultRegisterer.Register(fundingFailures) })
	fundingFailures.WithLabelValues(reason).Inc()
}
