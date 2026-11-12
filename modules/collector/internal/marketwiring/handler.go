// Package marketwiring wires concrete data sources into common collection
// pipelines. Neither the scheduler nor marketfetch imports provider adapters.
package marketwiring

import (
	"fmt"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	// 各市场的标的规则在包初始化时登记（markets.Register）。Collector、云函数、collector-stream 三个入口都经过本包装配，
	// 所以在这里显式引用；新增市场时加一行。
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockhk"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

func NewHandler() *marketfetch.Handler {
	h := marketfetch.NewHandler()
	h.NewKlinePipeline = NewKlinePipeline
	h.ResolveSymbol = ResolveSymbol
	h.PlanPeriodValid = planPeriodValid
	return h
}

// ResolveSymbol 按市场的标的规则把标的 ID 换成传给数据源的代码。来源由任务或标签指定的（市场，品种），
// 数据源必须在行情源目录里登记过；按路由分配来源的不看标签的数据源。
func ResolveSymbol(provider, marketID, marketType, subjectID string) (string, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return "", err
	}
	market, instrument := marketdata.MarketID(marketID), rules.DefaultInstrumentType(marketType)
	if !catalog.Default().Routed(market, instrument) {
		if _, err := catalog.Default().Lookup(market, instrument, provider, ""); err != nil {
			return "", fmt.Errorf("数据源 %q 没有登记 %s 的 %s", provider, marketID, instrument)
		}
	}
	return marketfetch.DefaultProviderSymbol(marketID, marketType, subjectID)
}

// DefaultSourceID 返回（市场，数据源，品种）在行情源目录里登记的 source_id；没登记时为空。
func DefaultSourceID(marketID, provider, instrumentType string) string {
	entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), marketdata.InstrumentType(instrumentType), provider, "")
	if err != nil {
		return ""
	}
	return entry.Key.SourceID
}

// FeedLabels 返回 feed 指标里市场、数据源、路由标签允许的取值：市场取已登记的市场，数据源取行情源目录，
// 路由按目录生成——按路由分配的是 <市场>_<品种>_kline_<频率>，单一来源的是 <数据源>_<品种>_kline_<频率>
// （有路由文件的市场里不归路由管的品种，路由 ID 不带频率，不在允许的取值里，标签记为 unknown）；
// 标的快照的路由 ID 一并保留。
func FeedLabels() marketfetch.FeedLabels {
	labels := marketfetch.FeedLabels{Markets: markets.IDs()}
	add := func(list *[]string, value string) {
		if !slices.Contains(*list, value) {
			*list = append(*list, value)
		}
	}
	for _, entry := range catalog.Default().Entries() {
		add(&labels.Providers, entry.Key.ProviderID)
		owner := entry.Key.ProviderID + "_" + string(entry.Instrument)
		if entry.RouteSource {
			owner = string(entry.MarketID) + "_" + string(entry.Instrument)
			add(&labels.Routes, string(entry.MarketID)+"_instrument_v1")
		} else {
			add(&labels.Routes, owner+"_instrument_v1")
		}
		add(&labels.KlineRoutePrefixes, owner+"_kline_")
	}
	return labels
}

// planPeriodValid 是采集计划的周期过滤：周期开始时刻必须是该市场此频率的一个交易桶。连续交易的市场每个周期都执行；
// 交易时段市场在午休、收盘后、非交易日不执行。交易日历判断不了（超出覆盖期）时也不执行，避免制造无意义的提交。
func planPeriodValid(market marketfetch.PlanMarket, frequency string, period time.Time) bool {
	trading, err := markets.TradingBar(market.MarketID, frequency, period)
	return err == nil && trading
}
