package exchange

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// FundingRate 是交易所公布的一次永续合约资金费结算记录（公共数据，不需要密钥）。
type FundingRate struct {
	// ExchangeSymbol 是交易所原生合约代码，例如 BTCUSDT。
	ExchangeSymbol string
	// FundingTime 是交易所记录的结算时刻，原样保留，不一定正好是整点。
	FundingTime time.Time
	// Rate 是这次结算的资金费率，正数表示多头付给空头。
	Rate shared.Decimal
	// MarkPrice 是结算时的标记价格；交易所没给（老记录是空串）时为零，不能用来结算。
	MarkPrice shared.Decimal
}
