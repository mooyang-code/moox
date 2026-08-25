package logicalaccount

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount   = errors.New("trade: invalid logical account")
	ErrMembershipChange = errors.New("trade: logical account membership change requires pause")
	ErrInhomogeneous    = errors.New("trade: logical account members must be homogeneous")
	ErrRunnerOwnership  = errors.New("trade: logical account runner ownership conflict")
	ErrAutomationState  = errors.New("trade: invalid logical account automation state")
)

type AutomationState string

const (
	AutomationActive AutomationState = "ACTIVE"
	AutomationPaused AutomationState = "PAUSED"
)

type Account struct {
	SpaceID         string
	ID              string
	Name            string
	OwnerRunnerID   string
	ExecutionMode   exchange.ExecutionMode
	MarketType      exchange.MarketType
	SettlementAsset string
	AutomationState AutomationState
	PauseReason     string
}

type Member struct {
	SpaceID          string
	LogicalAccountID string
	TradingAccountID string
	Enabled          bool
	Priority         int
}

func New(
	spaceID string,
	id string,
	name string,
	mode exchange.ExecutionMode,
	market exchange.MarketType,
	settlementAsset string,
) (Account, error) {
	account := Account{
		SpaceID: spaceID, ID: id, Name: name,
		ExecutionMode: mode, MarketType: market,
		SettlementAsset: settlementAsset,
		AutomationState: AutomationPaused,
		PauseReason:     "new logical account",
	}
	if err := account.Validate(); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (a Account) Validate() error {
	if blank(a.SpaceID) || blank(a.ID) || blank(a.Name) ||
		!a.ExecutionMode.Valid() || !a.MarketType.Valid() ||
		blank(a.SettlementAsset) {
		return fmt.Errorf("%w: missing or unsupported required field", ErrInvalidAccount)
	}
	switch a.AutomationState {
	case AutomationActive:
		if !blank(a.PauseReason) {
			return fmt.Errorf("%w: active account cannot have a pause reason", ErrAutomationState)
		}
	case AutomationPaused:
		if blank(a.PauseReason) {
			return fmt.Errorf("%w: paused account requires a reason", ErrAutomationState)
		}
	default:
		return fmt.Errorf("%w: unsupported state %q", ErrAutomationState, a.AutomationState)
	}
	return nil
}

func (m Member) Validate() error {
	if blank(m.SpaceID) || blank(m.LogicalAccountID) ||
		blank(m.TradingAccountID) {
		return fmt.Errorf("%w: incomplete member", ErrInvalidAccount)
	}
	return nil
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
