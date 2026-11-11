package target

import (
	"errors"
	"fmt"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestAccountExecutionErrorDoesNotHideJoinedPersistenceErrors(t *testing.T) {
	sqlErr := errors.New("sqlite write failed")
	external := &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("order rejected")}
	for _, original := range []error{external, fmt.Errorf("submit: %w", external), orderapp.ErrInsufficientFunds} {
		converted := accountExecutionError(original)
		require.IsType(t, &AccountError{}, converted)
		require.ErrorIs(t, converted, original)
	}
	for _, original := range []error{sqlErr, errors.Join(external, sqlErr), errors.Join(orderapp.ErrInsufficientFunds, sqlErr)} {
		require.Same(t, original, accountExecutionError(original))
	}
	require.False(t, singleCauseIs(errors.Join(orderapp.ErrTargetExpired, sqlErr), orderapp.ErrTargetExpired))
	require.True(t, singleCauseIs(fmt.Errorf("submit: %w", orderapp.ErrTargetExpired), orderapp.ErrTargetExpired))
}

// 账户不可执行（含带阻断原因的包装）按账户级错误处理，不拖累整个目标执行器的就绪。
func TestAccountNotExecutableIsAccountError(t *testing.T) {
	for _, original := range []error{
		tradingaccount.ErrAccountNotExecutable,
		fmt.Errorf("%w：检测发现阻断项", tradingaccount.ErrAccountNotExecutable),
	} {
		converted := accountExecutionError(original)
		require.IsType(t, &AccountError{}, converted)
		require.ErrorIs(t, converted, tradingaccount.ErrAccountNotExecutable)
	}
	joined := errors.Join(tradingaccount.ErrAccountNotExecutable, errors.New("sqlite write failed"))
	require.Same(t, joined, accountExecutionError(joined))
}
