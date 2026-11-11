package holding

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type Holding struct {
	TradingAccountID, InstrumentID, ExchangeSymbol, Asset string
	Quantity, AverageCost, MarkPrice, MarketValue         shared.Decimal
	// Sellable 是可卖数量（快照里的可用余额）：A 股已扣掉冻结和当日买入（T+1）。
	Sellable      shared.Decimal
	UnrealizedPnL *shared.Decimal
	SourceTime    time.Time
}
