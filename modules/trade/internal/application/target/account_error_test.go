package target

import (
	"errors"
	"fmt"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
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
