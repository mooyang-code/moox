package tradingstatus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/mooyang-code/moox/packages/events"
)

// 股票的三个市场都用腾讯实时快照第 40 个字段判定（A 股、美股：S 停牌，D、C 退市；港股：非空即停牌）。
const (
	MarketCN = "cn"
	MarketHK = "hk"
	MarketUS = "us"
)

// tencentSnapshot 是 cnstock.TencentClient 用到的三个方法，便于测试替换。
type tencentSnapshot interface {
	Snapshot(context.Context, []string) (map[string]cnstock.Quote, error)
	SnapshotHK(context.Context, []string) (map[string]cnstock.Quote, error)
	StatusUS(context.Context, []string) (map[string]cnstock.Quote, error)
}

// TencentSource 按腾讯快照判定股票的交易状态。每批 60 只单独请求：某一批失败只丢这一批（保留原值），不影响其他批。
type TencentSource struct {
	Client tencentSnapshot
	Market string
}

func (s TencentSource) Check(ctx context.Context, subjects []string) (map[string]string, error) {
	out := make(map[string]string, len(subjects))
	var failures error
	for start := 0; start < len(subjects); start += cnstock.TencentBatchLimit {
		batch := subjects[start:min(start+cnstock.TencentBatchLimit, len(subjects))]
		var quotes map[string]cnstock.Quote
		var err error
		switch s.Market {
		case MarketCN:
			quotes, err = s.Client.Snapshot(ctx, batch)
		case MarketHK:
			quotes, err = s.Client.SnapshotHK(ctx, batch)
		case MarketUS:
			quotes, err = s.Client.StatusUS(ctx, batch)
		default:
			return nil, fmt.Errorf("unknown tencent market %q", s.Market)
		}
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("tencent %s batch at %d: %w", s.Market, start, err))
			continue
		}
		for _, id := range batch {
			quote, ok := quotes[id]
			if !ok {
				continue
			}
			switch {
			case quote.Delisted:
				out[id] = Delisted
			case quote.Halted:
				out[id] = Suspended
			default:
				out[id] = Trading
			}
		}
	}
	return out, failures
}

// InstrumentLister 取交易所的交易对列表（带原始交易状态），由标的同步的 lister 提供。
type InstrumentLister func(ctx context.Context) ([]marketdata.Instrument, error)

// DelistedSymbols 返回已到期的下架代码（交易所写法），来自下架检测的记录。
type DelistedSymbols func(ctx context.Context, now time.Time) (map[string]bool, error)

// 币安永续进入交割、结算的原始状态：已经不能交易，也不会恢复，记退市。
var binanceSwapDelisted = map[string]bool{"PRE_DELIVERING": true, "DELIVERING": true, "DELIVERED": true, "PRE_SETTLE": true, "SETTLING": true, "CLOSE": true}

// BinanceSource 按币安 exchangeInfo 的原始状态判定，顺序固定、先命中的为准：
//  1. TRADING 记 trading，不看下架名单也不看合约类型（演练时手工登记过的、下架后重新上线的都不会被误标）；
//  2. 永续处于交割、结算状态记 delisted；
//  3. 现货非 TRADING、而且下架记录里这个交易对最新一行的下架时间已过，记 delisted（交易所会把下架的现货长期留在列表里，
//     靠这一条让标签同步最终把它移出）；
//  4. 其余非 TRADING（BREAK、HALT、PENDING_TRADING……）记 suspended。
//
// 列表里已经没有的交易对不在结果里（保留原值，由标签同步移出）。
type BinanceSource struct {
	MarketType string // spot 或 swap
	List       InstrumentLister
	Delisted   DelistedSymbols
	Now        func() time.Time
}

func (s BinanceSource) Check(ctx context.Context, subjects []string) (map[string]string, error) {
	instruments, err := s.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance %s instruments: %w", s.MarketType, err)
	}
	var delisted map[string]bool
	if s.MarketType == "spot" && s.Delisted != nil {
		now := time.Now().UTC()
		if s.Now != nil {
			now = s.Now().UTC()
		}
		if delisted, err = s.Delisted(ctx, now); err != nil {
			return nil, fmt.Errorf("binance spot delist notices: %w", err)
		}
	}
	byID := make(map[string]marketdata.Instrument, len(instruments))
	for _, item := range instruments {
		byID[item.SubjectID] = item
	}
	out := make(map[string]string, len(subjects))
	for _, id := range subjects {
		item, ok := byID[id]
		if !ok || item.TradingStatus == "" {
			continue
		}
		status := strings.ToUpper(item.TradingStatus)
		switch {
		case status == marketdata.BinanceTrading:
			out[id] = Trading
		case s.MarketType == "swap" && binanceSwapDelisted[status]:
			out[id] = Delisted
		case s.MarketType == "spot" && delisted[strings.ToUpper(item.ProviderSymbol)]:
			out[id] = Delisted
		default:
			out[id] = Suspended
		}
	}
	return out, nil
}

// 下架记录里现货的市场与范围（PAPER_ONLY 只影响模拟盘，不算交易所下架）。
const (
	DelistMarketSpot = events.DelistMarketSpot
	DelistScopeAll   = events.DelistScopeAll
)
