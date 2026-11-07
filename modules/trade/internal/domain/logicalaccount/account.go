package logicalaccount

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount   = errors.New("组合账户参数不合法")
	ErrMembershipChange = errors.New("修改组合成员前要先暂停组合")
	ErrInhomogeneous    = errors.New("组合成员必须同为实盘或同为模拟盘、同一环境、同一结算资产")
	// ErrMemberElsewhere：一个执行账户最多属于一个组合账户，换组合要先从原组合移除。
	ErrMemberElsewhere = errors.New("执行账户已属于其他组合账户，先从该组合移除")
	ErrRunnerOwnership = errors.New("组合账户已被别的策略会话占用")
	ErrAutomationState = errors.New("组合账户的自动执行状态不合法")
)

type AutomationState string

type ControlMode string

const (
	ControlStrategy ControlMode = "STRATEGY"
	ControlManual   ControlMode = "MANUAL"
)

const (
	AutomationActive AutomationState = "ACTIVE"
	AutomationPaused AutomationState = "PAUSED"
)

type Account struct {
	ControlMode     ControlMode
	SpaceID         string
	ID              string
	Name            string
	ExecutionMode   exchange.ExecutionMode
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
	settlementAsset string,
	controlMode ControlMode,
) (Account, error) {
	if controlMode == "" {
		controlMode = ControlStrategy
	}
	account := Account{
		ControlMode: controlMode,
		SpaceID:     spaceID, ID: id, Name: name,
		ExecutionMode:   mode,
		SettlementAsset: settlementAsset,
		AutomationState: AutomationPaused,
		PauseReason:     "新建的组合账户，等待恢复自动执行",
	}
	if err := account.Validate(); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (a Account) Validate() error {
	if a.ControlMode != ControlStrategy && a.ControlMode != ControlManual {
		return fmt.Errorf("%w: unsupported control mode %q", ErrInvalidAccount, a.ControlMode)
	}
	if a.ControlMode == ControlManual && a.AutomationState == AutomationActive {
		return fmt.Errorf("%w: manual account cannot have strategy ownership or automation", ErrInvalidAccount)
	}
	if blank(a.SpaceID) || blank(a.ID) || blank(a.Name) ||
		!a.ExecutionMode.Valid() ||
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
