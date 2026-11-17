package ashare

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/packages/cnstock"
)

// SettlementAsset 是 A 股的计价和结算资产。
const SettlementAsset = "CNY"

var (
	priceTick     = shared.MustDecimal("0.01")
	cageBuyRatio  = shared.MustDecimal("1.02")
	cageSellRatio = shared.MustDecimal("0.98")
)

// stockList 读完整的 A 股标的列表（东方财富）。
type stockList func(context.Context) ([]cnstock.Stock, error)

// NewMarket 用腾讯快照和东方财富列表构造 A 股公共行情；client 为空时用各自的默认超时。
func NewMarket(client *http.Client, seed stockfeed.Seed) *stockfeed.Market {
	tencent := cnstock.TencentClient{HTTPClient: client}
	return newMarket(tencent.Snapshot, func(ctx context.Context) ([]cnstock.Stock, error) { return cnstock.ListAllStocks(ctx, client) }, seed)
}

func newMarket(quotes stockfeed.QuoteSource, list stockList, seed stockfeed.Seed) *stockfeed.Market {
	market := &stockfeed.Market{Exchange: exchange.ExchangeAShare, Label: "A 股", Quotes: quotes, Seed: seed}
	market.List = func(ctx context.Context) ([]exchange.Instrument, error) {
		stocks, err := list(ctx)
		if err != nil {
			return nil, err
		}
		at := time.Now().UTC()
		if market.Now != nil {
			at = market.Now().UTC()
		}
		instruments := make([]exchange.Instrument, 0, len(stocks))
		for _, stock := range stocks {
			instrument, err := stockInstrument(stock.InstrumentID, at)
			if err != nil {
				return nil, err
			}
			instruments = append(instruments, instrument)
		}
		return instruments, nil
	}
	return market
}

// stockInstrument 按代码前缀定交易规则：科创板最少 200 股、按 1 股递增，北交所最少 100 股、按 1 股递增，其余 100 股一手；
// 价格步长 0.01 元；价格笼子的比例部分是基准价 ±2%，“10 个最小价位”在场所规则里（exchange.ExchangeRule.PriceCageTicks）。
// 停牌不写进标的状态（停牌持仓仍要算进组合），只在报价上标记。
func stockInstrument(instrumentID string, at time.Time) (exchange.Instrument, error) {
	board, err := cnstock.BoardOf(instrumentID)
	if err != nil {
		return exchange.Instrument{}, err
	}
	lot := cnstock.LotRuleFor(board)
	return exchange.Instrument{
		Exchange: exchange.ExchangeAShare, MarketType: exchange.MarketTypeStock,
		ExchangeSymbol: instrumentID, InstrumentID: instrumentID,
		BaseAsset: instrumentID, QuoteAsset: SettlementAsset, SettlementAsset: SettlementAsset,
		ExchangeQuantityStep: shared.MustDecimal(fmt.Sprint(lot.Step)),
		MinExchangeQuantity:  shared.MustDecimal(fmt.Sprint(lot.Minimum)),
		PriceTick:            priceTick, MinNotional: shared.Zero(),
		MaxBuyPriceRatio: cageBuyRatio, MinSellPriceRatio: cageSellRatio,
		Status: "TRADING", ExchangeUpdatedAt: at,
	}, nil
}
