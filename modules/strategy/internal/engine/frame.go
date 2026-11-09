// Package engine 是策略求值引擎：输入一帧数据与前序状态，输出目标权重、规则状态、摘要与逐标的解释。
// 它是纯函数：不读 Storage、不读时间、不写库，实盘与回放共用同一条路径。
package engine

import (
	"encoding/json"
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

// MarketInput 是一个市场的 View 本期输入：每个标的在该市场的行，以及该市场上游失败的标的。
type MarketInput struct {
	Rows map[string]Row
	// Failed 记录该市场上游失败的标的：列名 → 标的 → 原因（FailedFactor、FailedSource 或 FailedMarket）。
	Failed map[string]map[string]string
	// AgedOut 是这个市场里年龄不够（min_age_bars 的探针没通过）的标的：多个市场时，年龄按市场各自判断，老币刚上线永续，
	// 不能用现货的历史让永续的行通过年龄检查。选币池构造不取这些标的在这个市场的行，但已持仓的豁免：年龄剔除只拦新进入的标的，
	// 持仓在建仓时已满足年龄，不能因为探针窗口里的历史缺口把行删掉（删了就是无行、不冻结、被卖出）。没有年龄要求时为空。
	AgedOut map[string]struct{}
}

// Frame 是一个周期的全部输入。集合划分由 input 包完成到规则级的预期集合：
// Universe 是基础集合 U，Expected 与 AgedOut 按规则 id 给出 E(r) 与 G(r)；
// 可用集合 A(r) 与缺数集合 M(r) 由引擎按规则引用的列从 Rows 与 FailedColumns 推导。
//
// 调用方只填 Markets（策略涉及的每个市场一项，单市场策略只有一项）；Evaluate 开头做选币池构造，
// 为每个标的选出一行写入 Rows、RowMarket，并合并失败证据写入 FailedColumns，规则只看这几项。
type Frame struct {
	BarEnd time.Time
	// BarIndex 是该 bar 在日历中的序号，用于 holding 的换仓、止损冷却与 hold 的持有计数。
	BarIndex int64
	Markets  map[string]MarketInput
	Universe []string
	Expected map[string][]string
	AgedOut  map[string][]string
	// AgedOutMax 是 AgedOut 里因超过 max_age_bars（不再是新标的）剔除的部分，明细原因记 aged_out_max；其余记 min_age_bars。
	AgedOutMax map[string][]string
	// Holdings 是写了 holding.anchor 或 holding.calendar 的规则的换仓时钟（规则 id → 时钟），由输入层按交易日历算好；
	// 其余规则不出现，引擎按锚点 0 与 bars/offsets 计算。
	Holdings map[string]HoldingClock

	// 以下由选币池构造填入。Rows 每个标的一行，RowMarket 是这一行所属的市场；FailedColumns 是合并后的失败证据
	// （列名 → 标的 → 原因），引用该列的规则把这些标的视为缺数，明细原因为“<原因>:<列>”。
	Rows          map[string]Row
	RowMarket     map[string]string
	FailedColumns map[string]map[string]string
	// avail 是标的在各市场本期是否可用：该市场的 View 本期有它的行。
	avail map[string]map[string]bool
	// shortNoRow 是空头规则里按无行处理的持仓（规则 → 标的）：永续已经没有它、也没有失败证据的永续空头（见 restrictShortCandidates）。
	shortNoRow map[string]map[string]struct{}
}

// 上游失败的原因：因子对该标的计算失败，View 本身（例如 K 线采集）声明该标的本期失败，
// 或该标的所在市场本期整体不可用（FailedMarket：那个市场的因子本期被跳过）。只有市场不可用允许已持仓的标的冻结（见 freeze.go）。
const (
	FailedFactor = "factor_failed"
	FailedSource = "source_failed"
	FailedMarket = "market_unavailable"
)

// HoldingClock 是一条 holding 规则本根的换仓时钟。Anchor 是 holding.anchor 换成的 bar 序号（bars/offsets 写法的相位起点，
// 没写为 0）；Scheduled 是日历写法每个批次（holding.days 的值）最近一个应建仓 bar 的序号（不晚于本根），还没有过应建仓 bar 的批次不出现。
type HoldingClock struct {
	Anchor    int64
	Scheduled map[int]int64
}

// HoldingBatch 是 holding 规则在某个偏移建立的一个批次；BaseWeights 之和不超过 1，
// filter_after 剔除与缺数移除的份额留作现金。
type HoldingBatch struct {
	Offset         int               `json:"offset"`
	EstablishedBar int64             `json:"established_bar"`
	BaseWeights    map[string]string `json:"base_weights"`
	// Entries 是批次成员实际进入该批次时的入场价与最有利价格（规则开了 stop 时才记录）；补建批次的 EstablishedBar
	// 会回填到 offset 对齐的旧序号，不能用它取入场价。
	Entries map[string]StopEntry `json:"entries,omitempty"`
}

// RuleState 是一条规则下一期需要的理论状态：rank 规则记录本期持有（供 buffer 使用）与批次，
// signal 规则记录理论持仓。它不包含任何真实账户信息。
type RuleState struct {
	Held    []string       `json:"held,omitempty"`
	Batches []HoldingBatch `json:"batches,omitempty"`
	// Entries 是 rank（无 holding）与 signal 规则持有标的的入场价与最有利价格；Cooldown 是冷却截止的 bar 序号（含）。
	// 只在规则开了 stop 时记录（见 stop.go）。
	Entries  map[string]StopEntry `json:"entries,omitempty"`
	Cooldown map[string]int64     `json:"cooldown,omitempty"`
	// Tranche 是 tranche 规则的开仓记录（见 tranche.go）。
	Tranche *TrancheState `json:"tranche,omitempty"`
	// Hold 是 rank 规则 hold 块的入场序号、冷却与上次判进出场的序号（见 hold.go）。
	Hold *HoldState `json:"hold,omitempty"`
}

// State 是一次 ok 决策之后的全部状态：各规则状态与组合目标权重（供换手统计）。
type State struct {
	Rules map[string]RuleState `json:"rules,omitempty"`
	// Targets 是组合目标权重，键是标的与市场（见 TargetKey）：同一个币的现货与永续各自独立、不互相抵消。
	Targets map[string]string `json:"targets,omitempty"`
	// Members 与 Pool 只有策略池用：Members 是成员 id → 成员自己的引擎状态（规则状态与成员目标），按成员隔开，规则 id 不会撞名；
	// Pool 是池的跨期状态（上期比例、成员净值序列与模拟账本），由 pool 包编解码。
	Members map[string]State `json:"members,omitempty"`
	Pool    json.RawMessage  `json:"pool,omitempty"`
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

// Target 是一个标的在一个市场的目标权重（相对总权益，做空为负）。
type Target struct {
	InstrumentID string
	// Market 是目标所在的市场：spot、swap 或 equity。
	Market string
	Weight quant.Decimal
	// Hold 为 true 表示保持该标的当前持仓数量不动：Weight 是预留的预算占用（取上期组合目标权重），下游不据此调仓。
	Hold bool
}

// RuleSummary 是一条规则各阶段的数量与预算使用。
type RuleSummary struct {
	Expected  int `json:"expected"`
	AgedOut   int `json:"aged_out"`
	Available int `json:"available"`
	Missing   int `json:"missing"`
	// Held 是因所在市场本期不可用（或只缺上一根）而保持持仓的标的数（不计入 Available 与 Missing）。
	Held      int    `json:"held,omitempty"`
	Filtered  int    `json:"filtered"`
	Scored    int    `json:"scored"`
	Selected  int    `json:"selected"`
	Weighted  int    `json:"weighted"`
	Budget    string `json:"budget"`
	Allocated string `json:"allocated,omitempty"`
	// 本期触发的持仓单位数（不是标的数）：固定止损、跟踪止损、止盈。
	StopLoss     int `json:"stop_loss,omitempty"`
	TrailingStop int `json:"trailing_stop,omitempty"`
	TakeProfit   int `json:"take_profit,omitempty"`
}

// Summary 是本期摘要。
type Summary struct {
	Universe int                    `json:"universe"`
	Rules    map[string]RuleSummary `json:"rules"`
	Gross    string                 `json:"gross,omitempty"`
	Net      string                 `json:"net,omitempty"`
	// SpotNet 与 SwapNet 是混合市场策略分市场的净敞口；此时 Cash 没有意义，不填。
	SpotNet  string   `json:"spot_net,omitempty"`
	SwapNet  string   `json:"swap_net,omitempty"`
	Cash     string   `json:"cash,omitempty"`
	Turnover string   `json:"turnover,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	// Pool 是策略池本期的明细（成员净值、分配比例、择时与贡献），非池为空。
	Pool *PoolSummary `json:"pool,omitempty"`
}

// PoolSummary 是策略池一期的明细。
type PoolSummary struct {
	// Method 是分配方式（fixed 或 rotation）；Timing 为真表示叠加了择时。
	Method string `json:"method"`
	Timing bool   `json:"timing,omitempty"`
	// Members 按池里的成员顺序。
	Members []PoolMemberSummary `json:"members"`
}

// PoolMemberSummary 是一个成员一期的明细。
type PoolMemberSummary struct {
	ID string `json:"id"`
	// Status 是成员本期的求值结果：ok；skipped 表示本期没通过（沿用上一期的目标权重），Reason 与 Detail 说明原因。
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Equity 是成员模拟账本本期末的净值（初始为 1）；Bankrupt 为真表示成员模拟爆仓，此后不再分配资金。
	Equity   float64 `json:"equity"`
	Bankrupt bool    `json:"bankrupt,omitempty"`
	// Base 是分配方式给出的比例（择时与步长之前）；Timing 是择时系数（1 或 0，没有择时时为空）；RatioBefore 是乘择时之后、
	// 步长限制之前的比例；Ratio 是步长限制之后、实际用于合成的比例。
	Base        string `json:"base"`
	Timing      string `json:"timing,omitempty"`
	RatioBefore string `json:"ratio_before"`
	Ratio       string `json:"ratio"`
	// Rank 是轮动的综合名次分（各因子名次按权重加权，越小越靠前），只有 rotation 且历史足够时才有。
	Rank *float64 `json:"rank,omitempty"`
	// Gross 与 Net 是成员目标乘比例之后的总敞口与净敞口；Targets 是各（标的|市场）的贡献权重（比例 × 成员目标权重）。
	Gross   string            `json:"gross"`
	Net     string            `json:"net"`
	Targets map[string]string `json:"targets,omitempty"`
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
