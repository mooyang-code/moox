package execution

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type MarketQuote struct {
	Bid        shared.Decimal
	Ask        shared.Decimal
	Last       shared.Decimal
	SourceTime time.Time
}

type MarketDataSource interface {
	LoadInstruments(context.Context) ([]exchange.Instrument, error)
	GetQuote(context.Context, exchange.SymbolKey) (MarketQuote, error)
}

type ReferencePriceSource interface {
	GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error)
}

// FundingRateSource 是能查永续合约历史资金费率的场所才实现的公共行情能力（不需要密钥），模拟盘用它结算资金费。
type FundingRateSource interface {
	// ListFundingRates 返回 [start, end]（含两端）内 symbols 里这些合约的结算记录，不保证顺序；分页由实现负责，返回的是完整结果。
	// symbols 里合约的记录格式不对要报错；其他合约的记录不解析，格式不对只记日志跳过，不能让无关合约的坏数据挡住结算。
	ListFundingRates(ctx context.Context, start, end time.Time, symbols []string) ([]exchange.FundingRate, error)
}

// FundingRatesOf 返回适配器真实具备的资金费率来源：没有实现，或同一类型的适配器服务的市场没有资金费（例如币安现货）时返回 nil。
func FundingRatesOf(adapter any) FundingRateSource {
	source, ok := adapter.(FundingRateSource)
	if !ok {
		return nil
	}
	if capable, ok := adapter.(interface{ SupportsFundingRates() bool }); ok && !capable.SupportsFundingRates() {
		return nil
	}
	return source
}
