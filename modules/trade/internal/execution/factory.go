package execution

import (
	"fmt"
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

func (f Factory) Bind(account tradingaccount.Account, credential exchange.Credential) (ExecutionBundle, error) {
	if account.ExecutionMode == exchange.ExecutionModePaper {
		if f.BindPaper == nil {
			return ExecutionBundle{}, fmt.Errorf("execution: paper binder is not configured")
		}
		return f.BindPaper(account)
	}
	if f.BindLive == nil {
		return ExecutionBundle{}, fmt.Errorf("execution: live binder is not configured")
	}
	return f.BindLive(account, credential)
}
