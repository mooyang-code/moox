package marketfetch

import (
	"fmt"
	"sort"
	"strings"
)

// 行情源按出口 IP 的每分钟权重限额与单次 K 线请求的权重（Binance；其他行情源不估算）。
const (
	binanceSpotWeightLimit  = 6000
	binanceSwapWeightLimit  = 2400
	binanceSpotKlineWeight  = 2
	binanceSwapKlineWeight  = 1
	rateBudgetWarnRatio     = 0.7
	defaultAssumedEgressIPs = 1
	rateBudgetWorstAttempts = 3
)

// RateEstimate 是某个市场在“所有频率同一分钟收盘”（整点最重）时对单个出口 IP 的权重估算。
type RateEstimate struct {
	MarketType string
	Functions  int
	Requests   int // 峰值一分钟内的请求数（各数据集标的数之和，所有函数合计）
	Weight     int // 单次请求权重
	Limit      int // 每个 IP 每分钟限额
	IPs        int // 假设的出口 IP 个数
	// PerIPTypical 是每个 IP 一次尝试的权重，PerIPWorst 按每个请求用满全部尝试次数估算。
	PerIPTypical, PerIPWorst int
}

// Exceeds 表示最坏情况超过限额的 70%。
func (e RateEstimate) Exceeds() bool {
	return float64(e.PerIPWorst) > float64(e.Limit)*rateBudgetWarnRatio
}

func (e RateEstimate) String() string {
	return fmt.Sprintf("market=%s functions=%d peak_requests=%d weight=%d ips=%d per_ip_typical=%d per_ip_worst=%d limit=%d",
		e.MarketType, e.Functions, e.Requests, e.Weight, e.IPs, e.PerIPTypical, e.PerIPWorst, e.Limit)
}

// EstimateRateBudget 按期望状态估算每个市场对单个出口 IP 的峰值权重。
// 估算只依赖函数个数与标的数：请求总数 = Σ 数据集标的数（整点 1m 与 1h 在同一分钟），
// 每个函数一分钟内能发出的请求受并发度限制，不会超过它自己负责的标的数，所以总量与函数个数的多少无关，
// 函数越多、出口 IP 越分散（ips 越大）每个 IP 的压力越小。ips 由配置给出（保守默认 1，用 probe-egress 实测后调大）。
func EstimateRateBudget(functions map[string]*PlanFunction, ips int) []RateEstimate {
	if ips < 1 {
		ips = defaultAssumedEgressIPs
	}
	byMarket := map[string]*RateEstimate{}
	for _, function := range functions {
		if function == nil || function.Plan == nil || !strings.EqualFold(function.Plan.Market.Provider, "binance") {
			continue
		}
		marketType := strings.ToLower(function.Plan.Market.MarketType)
		weight, limit := binanceSpotKlineWeight, binanceSpotWeightLimit
		if marketType == "swap" {
			weight, limit = binanceSwapKlineWeight, binanceSwapWeightLimit
		}
		estimate := byMarket[marketType]
		if estimate == nil {
			estimate = &RateEstimate{MarketType: marketType, Weight: weight, Limit: limit, IPs: ips}
			byMarket[marketType] = estimate
		}
		estimate.Functions++
		for _, dataset := range function.Plan.Datasets {
			if len(dataset.Copies) > 0 {
				estimate.Requests += len(dataset.Copies[len(dataset.Copies)-1].Subjects)
			}
		}
	}
	out := make([]RateEstimate, 0, len(byMarket))
	for _, estimate := range byMarket {
		total := estimate.Requests * estimate.Weight
		estimate.PerIPTypical = (total + ips - 1) / ips
		estimate.PerIPWorst = estimate.PerIPTypical * rateBudgetWorstAttempts
		out = append(out, *estimate)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarketType < out[j].MarketType })
	return out
}
