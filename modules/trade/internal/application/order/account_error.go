package order

import "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"

// AccountExecutionError identifies a failed account dependency, not a failed
// persistence operation. Callers can isolate this account while keeping shared
// storage failures visible to worker readiness.
type AccountExecutionError struct {
	TradingAccountID string
	Operation        string
	Err              error
}

func (e *AccountExecutionError) Error() string { return e.Err.Error() }
func (e *AccountExecutionError) Unwrap() error { return e.Err }

func accountExecutionError(accountID, operation string, err error) error {
	if err == nil {
		return nil
	}
	if paper.IsInfrastructureError(err) {
		return err
	}
	return &AccountExecutionError{TradingAccountID: accountID, Operation: operation, Err: err}
}
