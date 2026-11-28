package logicalaccount

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestValidateConfigRejectsInvalidFieldsAndControlMode(t *testing.T) {
	require.NoError(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "USER", DefaultPauseReason, ""))
	require.NoError(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentTestnet, "USDT", ControlStrategy, "", "", ""))
	require.NoError(t, ValidateConfig("space-1", "logical-1", "manual", exchange.AccountEnvironmentProduction, "USDT", ControlManual, "", "", ""))

	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentUnspecified, "USDT", ControlStrategy, "", "", ""), ErrInvalidAccount)
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", "UNKNOWN", "", "", ""), ErrInvalidAccount)
	require.ErrorIs(t, ValidateConfig("", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "", "", ""), ErrInvalidAccount)
}

func TestValidateConfigEnforcesPauseInvariants(t *testing.T) {
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "manual", exchange.AccountEnvironmentPaper, "USDT", ControlManual, "USER", "人工暂停", ""), ErrAutomationState)
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "USER", "", ""), ErrAutomationState)
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "", "孤立原因", ""), ErrAutomationState)
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "MARGIN", "保证金低", ""), ErrAutomationState)
	require.ErrorIs(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentPaper, "USDT", ControlStrategy, "USER", "人工暂停", "REDUCED"), ErrAutomationState)
	require.NoError(t, ValidateConfig("space-1", "logical-1", "main", exchange.AccountEnvironmentProduction, "USDT", ControlStrategy, "MARGIN", "保证金低", "REDUCED"))
}
