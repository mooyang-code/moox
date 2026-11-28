package logicalaccount

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount   = errors.New("组合账户参数不合法")
	ErrMembershipChange = errors.New("策略驱动的组合账户在修改成员前要先暂停")
	ErrInhomogeneous    = errors.New("组合成员必须与组合同一环境、同一结算资产")
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

// DefaultPauseReason 是新建策略驱动组合时的初始暂停原因。
const DefaultPauseReason = "新建的组合账户，等待恢复自动执行"

// ValidateConfig 校验组合账户配置与暂停状态不变量：
// 自主下单组合不带暂停状态（暂停来源、原因与减仓状态恒为空）；
// 策略组合的暂停来源与暂停原因同时为空或同时非空，保证金减仓状态仅在来源为 MARGIN 时非空。
func ValidateConfig(
	spaceID string,
	id string,
	name string,
	environment exchange.AccountEnvironment,
	settlementAsset string,
	controlMode ControlMode,
	pauseSource string,
	pauseReason string,
	marginReduceState string,
) error {
	if controlMode != ControlStrategy && controlMode != ControlManual {
		return fmt.Errorf("%w: 不支持的控制方式 %q", ErrInvalidAccount, controlMode)
	}
	if blank(spaceID) || blank(id) || blank(name) ||
		!environment.Valid() ||
		blank(settlementAsset) {
		return fmt.Errorf("%w: 缺少必填字段或取值不受支持", ErrInvalidAccount)
	}
	if controlMode == ControlManual {
		if !blank(pauseSource) || !blank(pauseReason) || !blank(marginReduceState) {
			return fmt.Errorf("%w: 自主下单组合不带暂停状态", ErrAutomationState)
		}
		return nil
	}
	if blank(pauseSource) != blank(pauseReason) {
		return fmt.Errorf("%w: 暂停来源与暂停原因必须同时为空或同时填写", ErrAutomationState)
	}
	if (pauseSource == "MARGIN") != !blank(marginReduceState) {
		return fmt.Errorf("%w: 保证金减仓状态仅在保证金风控暂停时填写", ErrAutomationState)
	}
	return nil
}

// ValidateMember 校验组合账户成员标识是否完整。
func ValidateMember(spaceID, logicalAccountID, tradingAccountID string) error {
	if blank(spaceID) || blank(logicalAccountID) || blank(tradingAccountID) {
		return fmt.Errorf("%w: 组合成员缺少必填标识", ErrInvalidAccount)
	}
	return nil
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
