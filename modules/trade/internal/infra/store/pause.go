package store

// PauseSource 是组合暂停的来源，存在 t_logical_accounts.c_pause_source；运行中为空。
type PauseSource string

const (
	// PauseSourceUser 是人工暂停：用户手动暂停、人工下单、撤单、清仓，以及新建组合时的初始暂停。
	PauseSourceUser PauseSource = "USER"
	// PauseSourceSystem 是系统自动暂停（外部订单、逐仓持仓、账户检测、收敛异常等），要告警。
	PauseSourceSystem PauseSource = "SYSTEM"
	// PauseSourceMargin 是保证金率风控暂停，同样算系统自动暂停，另有减仓状态。
	PauseSourceMargin PauseSource = "MARGIN"
)

// MarginReduceState 是保证金率风控暂停时的减仓状态，存在 t_logical_accounts.c_margin_reduce_state；其他暂停与运行中为空。
type MarginReduceState string

const (
	// MarginReduced：已暂停并发起按比例减仓。
	MarginReduced MarginReduceState = "REDUCED"
	// MarginReducePending：已暂停，减仓排不进去（有下架清仓在跑），等下一轮补上。
	MarginReducePending MarginReduceState = "PENDING"
	// MarginUnreduced：减仓待执行期间保证金率回到安全线，没有减仓，等用户确认后恢复。
	MarginUnreduced MarginReduceState = "UNREDUCED"
	// MarginReduceFailed：风控减仓以 FAILED 结束。
	MarginReduceFailed MarginReduceState = "FAILED"
	// MarginReducePartial：风控减仓以 PARTIAL 结束，只减了一部分。
	MarginReducePartial MarginReduceState = "PARTIAL"
)

// Pause 是组合的暂停状态：来源与减仓状态供程序判断，原因只用于展示。零值表示运行中（未暂停）。
type Pause struct {
	Source       PauseSource
	MarginReduce MarginReduceState
	Reason       string
}

// autoPausePrefix 是系统自动暂停原因的展示前缀，让用户一眼区分人工暂停；程序不靠它判断。
const autoPausePrefix = "自动暂停："

// marginPauseText 是保证金率风控暂停原因的开头：原因文字固定、不带比值，因为暂停原因会作为 auto_paused 告警的详情上报，
// 带变化的数字会让每次触发都产生新的指标序列。
const marginPauseText = "保证金率过低"

// marginPauseReasons 是各减仓状态对应的暂停原因（展示用），marginAttentionDetails 是 margin_ratio_low 关注项的固定文字。
var (
	marginPauseReasons = map[MarginReduceState]string{
		MarginReduced:       "，组合已暂停并按设置的比例减仓，等待确认安全后手动恢复",
		MarginReducePending: "，组合已暂停，减仓待执行（等待进行中的清仓结束）",
		MarginUnreduced:     "，组合已暂停但没有减仓（保证金率已回到安全线），等待确认安全后手动恢复",
		MarginReduceFailed:  "，组合已暂停，风控减仓失败，请检查持仓后手动处理并确认恢复",
		MarginReducePartial: "，组合已暂停，风控减仓只完成了一部分，请检查持仓后手动处理并确认恢复",
	}
	marginAttentionDetails = map[MarginReduceState]string{
		MarginReduced:       "保证金率风控已暂停，等待确认后恢复",
		MarginReducePending: "保证金率风控已暂停，减仓待执行",
		MarginUnreduced:     "保证金率风控已暂停，没有减仓，等待确认后恢复",
		MarginReduceFailed:  "保证金率风控已暂停，减仓失败，需要人工处理",
		MarginReducePartial: "保证金率风控已暂停，减仓只完成一部分，需要人工处理",
	}
)

// UserPause 是人工暂停。
func UserPause(reason string) Pause {
	return Pause{Source: PauseSourceUser, Reason: reason}
}

// SystemPause 是系统自动暂停，原因加上“自动暂停：”前缀展示。
func SystemPause(reason string) Pause {
	return Pause{Source: PauseSourceSystem, Reason: autoPausePrefix + reason}
}

// MarginPause 是保证金率风控暂停，原因按减仓状态取固定文字。
func MarginPause(state MarginReduceState) Pause {
	return Pause{Source: PauseSourceMargin, MarginReduce: state, Reason: autoPausePrefix + marginPauseText + marginPauseReasons[state]}
}

// Auto 报告暂停是否来自系统（含保证金率风控），这类暂停要告警，也不被外部订单的原因覆盖。
func (p Pause) Auto() bool {
	return p.Source == PauseSourceSystem || p.Source == PauseSourceMargin
}

// Margin 报告暂停是否来自保证金率风控。
func (p Pause) Margin() bool { return p.Source == PauseSourceMargin }

// MarginAttentionDetail 是 margin_ratio_low 关注项的固定文字，带上减仓状态（不带比值）。
func MarginAttentionDetail(state MarginReduceState) string {
	if detail, ok := marginAttentionDetails[state]; ok {
		return detail
	}
	return marginAttentionDetails[MarginReduced]
}

func validPause(p Pause) bool {
	switch p.Source {
	case PauseSourceUser, PauseSourceSystem:
		return p.MarginReduce == "" && !blank(p.Reason)
	case PauseSourceMargin:
		_, ok := marginPauseReasons[p.MarginReduce]
		return ok && !blank(p.Reason)
	}
	return false
}
