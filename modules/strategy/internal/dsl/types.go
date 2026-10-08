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
	Name      string
	Bar       string // 可选断言，规范频率；实际周期由绑定的 View 决定
	Universe  Universe
	Rules     []Rule
	Portfolio Portfolio
}

// Universe 是策略级默认标的池：省略即 all。
type Universe struct {
	Tags        []string
	ExcludeTags []string
	Include     []string
	Exclude     []string
	MinAgeBars  int
}

// Pool 是规则级标的池。Explicit 为 true 时替代 universe 的标签与黑白名单。
type Pool struct {
	Explicit bool
	Fixed    []string
	Tags     []string
}

// Select 描述选择阶段：top 或 bottom 二选一，where 可选，buffer 是排名缓冲区。
type Select struct {
	Top    int
	Bottom int
	Where  string
	Buffer int
}

// Weight 描述配权：Total 是规则预算上限，Cap 是规则内单标的上限（HasCap 为 false 表示不限）。
type Weight struct {
	Total  quant.Decimal
	Method string
	Cap    quant.Decimal
	HasCap bool
}

// Holding 是 rank 规则的分批换仓：每 Bars 根为一个周期，在 Offsets 指定的偏移处重建对应批次。
type Holding struct {
	Bars    int
	Offsets []int
}

// Rule 是一条规则。rank 规则使用 Filter、Score、Select、Weight、FilterAfter、Holding；
// signal 规则使用 Pool、Entry、Exit、Weight。
type Rule struct {
	ID          string
	Name        string
	Type        string
	Pool        Pool
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
