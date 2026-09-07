package accountsync

import (
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
)

// Apply only at adapter calls. The caller may join a subsequent readiness
// write failure, which must remain a separate shared-infrastructure cause.
func accountDependencyError(accountID, operation string, err error) error {
	if err == nil || paper.IsInfrastructureError(err) {
		return err
	}
	return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: operation, Err: err}
}
