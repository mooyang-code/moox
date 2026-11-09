// Package dsl 定义策略 DSL 的数据结构、严格解析、校验与表达式编译。
//
// DSL 只描述截面上的选股与配权逻辑：规则是列表，每条规则有稳定的 id、展示用的 name 和
// 固定词表的 type（rank 截面选股、signal 固定池择时）。时序计算一律在因子层完成，
// 策略表达式只在当期一帧数据上求值，可选地引用上一根（bars[-1]）。
package dsl

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

const (
	// RuleTypeRank 是截面选股规则：filter → score → select → weight → filter_after。
	RuleTypeRank = "rank"
	// RuleTypeSignal 是固定池择时规则：对旧理论持仓判 exit，再对候选判 entry。
	RuleTypeSignal = "signal"

	SideLong  = "long"
	SideShort = "short"

	// WeightMethodEqual 让选中标的平分规则预算。
	WeightMethodEqual = "equal"
	// WeightMethodRank 按名次线性配权：最优 N 份、最差 1 份，方向与 select 一致。
	WeightMethodRank = "rank"

	// DefaultMaxMissing 是缺数守门的默认阈值：缺数标的占预期集合的比例超过它则整期跳过。
	DefaultMaxMissing = "0.2"
)

// Strategy 是解析并校验后的策略定义。
type Strategy struct {
	Name string
	Bar  string // 可选断言，规范频率；实际周期由绑定的 View 决定
	// Market 是市场模式；DSL 没写 market 时为零值，由绑定的因子任务的市场填入默认值（见 Market）。
	Market Market
	// MarketDeclared 为真表示 DSL 里显式写了 market。
	MarketDeclared bool
	// ReferenceMarkets 是 market.reference 写出的参考市场（S3）：单市场策略额外绑定的另一个市场的任务，只用于 age_markets: all
	// 的年龄判定，不参与选择与下单。
	ReferenceMarkets []string
	Universe         Universe
	Rules            []Rule
	// Pool 非空表示策略池：与 Rules 互斥（见 pool.go）。
	Pool      *StrategyPool
	Portfolio Portfolio
	// FactorParams 是可变成员的成员键 → 参数名 → 下一期参数的输出表达式（见 factors.go）。
	FactorParams map[string]map[string]ParamExpr
	// Sources 是附加列来源（见 sources.go），按声明顺序。
	Sources []Source
}

// Universe 是策略级默认标的池：省略即 all。规则可以写规则级 universe 逐项覆盖（见 universe.go）。
// MinAgeBars、MaxAgeBars 是年龄下限与上限（只保留年龄小于上限的新标的）；AgeMarkets 为 all 时要求在全部绑定市场上判断年龄。
type Universe struct {
	Tags        []string
	ExcludeTags []string
	Include     []string
	Exclude     []string
	MinAgeBars  int
	MaxAgeBars  int
	AgeMarkets  string
}

// Pool 是规则级标的池。Explicit 为 true 时替代 universe 的标签与黑白名单。
type Pool struct {
	Explicit bool
	Fixed    []string
	Tags     []string
}

// Select 描述选择阶段（见 select.go）：top、bottom、range 三选一；top/bottom 是正整数个数或 (0, 1) 的比例，比例按
// CountRound 取整后与 CountMin 取大；Range 是名次闭区间（未写时为零值）；Group 限制每组入选个数；Then 在第一段的结果上二次排序。
// where 可选，buffer 是排名缓冲区。
type Select struct {
	Top        float64
	Bottom     float64
	CountRound string
	CountMin   int
	Range      [2]int
	Group      *SelectGroup
	Then       *SelectThen
	Where      string
	Buffer     int
}

// Weight 描述配权：Total 是规则预算上限，Cap 是规则内单标的上限（HasCap 为 false 表示不限）。
type Weight struct {
	Total  quant.Decimal
	Method string
	Cap    quant.Decimal
	HasCap bool
}

// Holding 是 rank 规则的分批换仓（见 holding.go）：每 Bars 根为一个周期，在 Offsets 指定的偏移处重建对应批次；
// 写了 Calendar 时改按交易日历换仓，Days 的每个值是一个批次。Anchor 是相位锚点（某根 bar 的 bar_start）。
type Holding struct {
	Bars     int
	Offsets  []int
	Anchor   string
	Calendar string
	Every    int
	Days     []int
	At       string
}

// Rule 是一条规则。rank 规则使用 Filter、Score、Select、Weight、FilterAfter、Holding；
// signal 规则使用 Pool、Entry、Exit、Weight；tranche 规则使用 Weight（只有 total）与 Tranche。
type Rule struct {
	ID   string
	Name string
	Type string
	Pool Pool
	// Universe 是规则级 universe（见 universe.go）；AllowEmpty 为真时这条规则不参与整期守门，候选为空或缺数时只是不出目标。
	Universe    *RuleUniverse
	AllowEmpty  bool
	Filter      string
	Score       string
	Select      Select
	HasSelect   bool
	Weight      Weight
	Side        string
	FilterAfter string
	Entry       string
	Exit        string
	Holding     *Holding
	// Hold 是 rank 规则的进出场持有（见 hold.go）。
	Hold *Hold
	// Scale 是规则输出权重上的仓位系数；OnMissing 是持有标的缺数时的冻结保持（见 scale.go，tranche 规则不支持 on_missing）。
	Scale     *Scale
	OnMissing *OnMissing
	// Stop 是 rank、signal 规则的止损止盈（见 stop.go）；tranche 规则写在子策略模板里。
	Stop *Stop
	// Tranche 是 tranche 规则的子策略模板与参数表（见 tranche.go）。
	Tranche *TrancheSpec
	// Market 是规则级市场（spot 或 swap，见 market.go）：策略涉及两个市场时，这条规则只从该市场的序列选择，目标也落在该市场；
	// 空串表示沿用策略的选币池与定市场。
	Market string
}

// Portfolio 是组合级约束与守门参数。
type Portfolio struct {
	Leverage     quant.Decimal
	MaxWeight    quant.Decimal
	HasMaxWeight bool
	MinUniverse  int
	MaxMissing   quant.Decimal
}

// Hash 返回 DSL 文本的内容哈希，作为定义版本标识。
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
