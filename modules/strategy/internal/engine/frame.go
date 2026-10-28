// Package engine 是策略求值引擎：输入一帧数据与前序状态，输出目标权重、规则状态、摘要与逐标的解释。
// 它是纯函数：不读 Storage、不读时间、不写库，实盘与回放共用同一条路径。
package engine

import (
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// Row 是一个标的在当期（以及可选的上一根）的列值。缺失的列不出现在 map 中，不补零。
// 列值用 float64 是因为表达式引擎按浮点求值；目标权重仍用定点 Decimal。
type Row struct {
	Values   map[string]float64
	Previous map[string]float64
	// Params 是这一行各可变成员这一期实际用的参数（成员键 → 参数名 → 值），来自 __params 列；表达式里的
	// params(成员键, 参数名) 读它，缺值时为 NaN（和缺数一样不满足比较）。
	Params map[string]map[string]float64
}

// Frame 是一个周期的全部输入。集合划分由 input 包完成到规则级的预期集合：
// Universe 是基础集合 U，Expected 与 AgedOut 按规则 id 给出 E(r) 与 G(r)；
// 可用集合 A(r) 与缺数集合 M(r) 由引擎按规则引用的列从 Rows 与 FailedColumns 推导。
type Frame struct {
	BarEnd time.Time
	// BarIndex 是该 bar 在日历中的序号，只用于 holding 的 offset 计算。
	BarIndex int64
	// Spot 为 true 时不允许负权重且 Σw 不得超过 1。
	Spot     bool
	Rows     map[string]Row
	Universe []string
	Expected map[string][]string
	AgedOut  map[string][]string
	// FailedColumns 记录上游失败的标的：列名 → 标的 → 原因（FailedFactor 或 FailedSource）。引用该列的规则把这些
	// 标的视为缺数，明细原因为“<原因>:<列>”。
	FailedColumns map[string]map[string]string
}

// 上游失败的原因：因子对该标的计算失败，因子因回看窗口 K 线空洞对该标的失败（FailedGap，是 FailedFactor 的细分），
// 或 View 本身（例如 K 线采集）声明该标的本期失败。
const (
	FailedFactor = "factor_failed"
	FailedGap    = "factor_gap"
	FailedSource = "source_failed"
)

// HoldingBatch 是 holding 规则在某个偏移建立的一个批次；BaseWeights 之和不超过 1，
// filter_after 剔除与缺数移除的份额留作现金。
type HoldingBatch struct {
	Offset         int               `json:"offset"`
	EstablishedBar int64             `json:"established_bar"`
	BaseWeights    map[string]string `json:"base_weights"`
}

// RuleState 是一条规则下一期需要的理论状态：rank 规则记录本期持有（供 buffer 使用）与批次，
// signal 规则记录理论持仓。它不包含任何真实账户信息。
type RuleState struct {
	Held    []string       `json:"held,omitempty"`
	Batches []HoldingBatch `json:"batches,omitempty"`
	// Tranche 是 tranche 规则的开仓记录（见 tranche.go）。
	Tranche *TrancheState `json:"tranche,omitempty"`
}

// State 是一次 ok 决策之后的全部状态：各规则状态与组合目标权重（供换手统计）。
type State struct {
	Rules   map[string]RuleState `json:"rules,omitempty"`
	Targets map[string]string    `json:"targets,omitempty"`
}

// 决策状态与跳过原因。
const (
	StatusOK      = "ok"
	StatusSkipped = "skipped"

	SkipNoData           = "no_data"
	SkipUniverseTooSmall = "universe_too_small"
	SkipTooManyMissing   = "too_many_missing"
	SkipConfigError      = "config_error"
)

// Target 是一个标的的目标权重（相对总权益，做空为负）。
type Target struct {
	InstrumentID string
	Weight       quant.Decimal
	// Hold 为 true 表示保持该标的当前持仓数量不动：Weight 是预留的预算占用（取上期组合目标权重），下游不据此调仓。
	Hold bool
}

// RuleSummary 是一条规则各阶段的数量与预算使用。
type RuleSummary struct {
	Expected  int `json:"expected"`
	AgedOut   int `json:"aged_out"`
	Available int `json:"available"`
	Missing   int `json:"missing"`
	// Held 是因 K 线空洞缺数而保持持仓的标的数（不计入 Available 与 Missing）。
	Held      int    `json:"held,omitempty"`
	Filtered  int    `json:"filtered"`
	Scored    int    `json:"scored"`
	Selected  int    `json:"selected"`
	Weighted  int    `json:"weighted"`
	Budget    string `json:"budget"`
	Allocated string `json:"allocated,omitempty"`
}

// Summary 是本期摘要。
type Summary struct {
	Universe int                    `json:"universe"`
	Rules    map[string]RuleSummary `json:"rules"`
	Gross    string                 `json:"gross,omitempty"`
	Net      string                 `json:"net,omitempty"`
	Cash     string                 `json:"cash,omitempty"`
	Turnover string                 `json:"turnover,omitempty"`
	Notes    []string               `json:"notes,omitempty"`
}

// Decision 是一次求值的产物。Status 为 skipped 时 Targets 为空、State 等于前序状态。
type Decision struct {
	Status     string
	SkipReason string
	Targets    []Target
	State      State
	Summary    Summary
	Items      []Item
}
