package execution

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type ExecutionBundle struct {
	Adapter            ExecutionAdapter
	AccountEvents      AccountEventSource
	MarketData         MarketDataSource
	ReservationPolicy  ReservationPolicy
	InstrumentResolver InstrumentResolver
}
type LiveBinder func(tradingaccount.Account, exchange.Credential) (ExecutionBundle, error)
type PaperBinder func(tradingaccount.Account) (ExecutionBundle, error)
type Factory struct {
	BindLive  LiveBinder
	BindPaper PaperBinder
}
