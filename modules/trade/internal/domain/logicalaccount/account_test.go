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
	)

	require.NoError(t, err)
	require.Equal(t, AutomationPaused, account.AutomationState)
	require.NotEmpty(t, account.PauseReason)
}

func TestLogicalAccountAutomationStateAndReasonMustAgree(t *testing.T) {
	valid := Account{
		SpaceID: "space-1", ID: "logical-1", Name: "main",
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

func TestLogicalAccountMembershipRequiresPause(t *testing.T) {
	account := Account{AutomationState: AutomationActive}
	require.ErrorIs(t, account.RequirePaused(), ErrMembershipChange)
	account.AutomationState = AutomationPaused
	require.NoError(t, account.RequirePaused())
}
