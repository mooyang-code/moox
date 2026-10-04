package logicalaccount

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestNewLogicalAccountStartsPaused(t *testing.T) {
	account, err := New(
		"space-1",
		"logical-1",
		"main",
		exchange.ExecutionModePaper,
		exchange.MarketTypeSpot,
		"USDT",
		"",
	)

	require.NoError(t, err)
	require.Equal(t, AutomationPaused, account.AutomationState)
	require.NotEmpty(t, account.PauseReason)
	require.Equal(t, ControlStrategy, account.ControlMode)
}

func TestManualAccountRejectsStrategyOwnershipAndAutomation(t *testing.T) {
	a, err := New("space", "logical", "manual", exchange.ExecutionModePaper, exchange.MarketTypeSpot, "USDT", ControlManual)
	require.NoError(t, err)
	require.Equal(t, ControlManual, a.ControlMode)
	require.NoError(t, a.Validate())
	a.OwnerRunnerID = "runner"
	require.Error(t, a.Validate())
	a.OwnerRunnerID = ""
	a.AutomationState, a.PauseReason = AutomationActive, ""
	require.Error(t, a.Validate())
	a.ControlMode = "invalid"
	require.Error(t, a.Validate())
}

func TestNewRejectsUnknownControlMode(t *testing.T) {
	_, err := New("space", "logical", "name", exchange.ExecutionModePaper, exchange.MarketTypeSpot, "USDT", "unknown")
	require.Error(t, err)
}

func TestLogicalAccountAutomationStateAndReasonMustAgree(t *testing.T) {
	valid := Account{
		ControlMode: ControlStrategy,
		SpaceID:     "space-1", ID: "logical-1", Name: "main",
		ExecutionMode: exchange.ExecutionModePaper,
		MarketType:    exchange.MarketTypeSpot, SettlementAsset: "USDT",
		AutomationState: AutomationPaused, PauseReason: "manual",
	}
	require.NoError(t, valid.Validate())

	active := valid
	active.AutomationState = AutomationActive
	require.ErrorIs(t, active.Validate(), ErrAutomationState)

	paused := valid
	paused.PauseReason = ""
	require.ErrorIs(t, paused.Validate(), ErrAutomationState)

	active.PauseReason = ""
	require.NoError(t, active.Validate())
}
