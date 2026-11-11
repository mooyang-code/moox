package target

import (
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
)

// AccountError identifies an account/quote or validation failure, not a failed
// SQLite operation. The worker can continue serving other logical accounts.
type AccountError struct {
	TradingAccountID string
	Err              error
}

func (e *AccountError) Error() string {
	if e.TradingAccountID == "" {
		return "目标执行：" + e.Err.Error()
	}
	return "执行账户 " + e.TradingAccountID + "：" + e.Err.Error()
}
func (e *AccountError) Unwrap() error { return e.Err }

// Do not classify a joined persistence failure by one business-error branch.
func singleCauseIs(err, cause error) bool {
	for err != nil {
		if err == cause {
			return true
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}

func accountExecutionError(err error) error {
	if _, ok := err.(*AccountError); ok {
		return err
	}
	if paper.IsInfrastructureError(err) {
		return err
	}
	if accountID, ok := accountCause(err); ok {
		return &AccountError{TradingAccountID: accountID, Err: err}
	}
	return err
}

func accountCause(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	switch cause := err.(type) {
	case *AccountError:
		return cause.TradingAccountID, true
	case *orderapp.AccountExecutionError:
		return cause.TradingAccountID, true
	case *exchange.Error:
		return "", true
	case interface{ Unwrap() []error }:
		causes := cause.Unwrap()
		if len(causes) == 0 {
			return "", false
		}
		var accountID string
		for _, child := range causes {
			id, ok := accountCause(child)
			if !ok {
				return "", false
			}
			if id != "" {
				accountID = id
			}
		}
		return accountID, true
	case interface{ Unwrap() error }:
		return accountCause(cause.Unwrap())
	default:
		switch err {
		case ErrInvalidTarget, ErrTargetSession, errTargetOrderUnresolved, orderdomain.ErrInvalidSpec,
			orderdomain.ErrReferencePriceStale, orderapp.ErrInstrumentDisabled,
			orderapp.ErrQuantityRule, orderapp.ErrNotionalLimit,
			orderapp.ErrInsufficientFunds, orderapp.ErrLeverageLimit, orderapp.ErrReduceOnly,
			// 账户不可执行（停用、未就绪、检测有阻断项）只影响这一个账户。
			tradingaccount.ErrAccountNotExecutable:
			return "", true
		}
	}
	return "", false
}
