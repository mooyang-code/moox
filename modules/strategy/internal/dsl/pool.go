package dsl

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// 策略池（设计 3.5）：一个策略要么写 rules，要么写 strategy_pool，两者互斥。池由若干成员策略组成，
// 每个成员独立求值并在模拟账本里维护自己的净值，池按固定比例或净值轮动分配资金，再叠加择时与步长限制。

const (
	// AllocationFixed 按 allocation.ratios 的固定比例分配。
	AllocationFixed = "fixed"
	// AllocationRotation 按成员净值序列上的轮动因子排名，取前 top 名平分。
	AllocationRotation = "rotation"

	// 轮动因子（都作用在成员净值序列上）。
	FactorReturn   = "return"   // 区间收益：净值 ÷ window 期之前的净值 − 1
	FactorMARatio  = "ma_ratio" // 净值 ÷ window 期均线 − 1
	FactorDrawdown = "drawdown" // window 期区间的最大回撤，越小越好

	// OrderAsc、OrderDesc 是轮动因子的排序方向：降序时因子值大的名次靠前。
	OrderAsc  = "asc"
	OrderDesc = "desc"

	// MaxPoolMembers 是策略池成员数的上限：每个成员每期各自求值并各自维护账本。
	MaxPoolMembers = 20
	// MaxPoolWindow 是轮动因子与择时窗口的上限（预热要把它们读满）。
	MaxPoolWindow = 2016
	// MaxPoolFeeBps 是成员账本手续费的上限（基点）。
	MaxPoolFeeBps = 1000

	// 成员账本手续费的默认值（基点），与研究回放界面的默认值一致。
	DefaultPoolSpotFeeBps = 10
	DefaultPoolSwapFeeBps = 5
)

// StrategyPool 是策略池的定义。
type StrategyPool struct {
	Members    []PoolMember
	Allocation PoolAllocation
	// Timing 非空时，成员净值在 window 期均线之上取 1，否则取 0。
	Timing *PoolTiming
	// Step 是每期每个成员的比例变化上限；HasStep 为假表示不限制。
	Step    quant.Decimal
	HasStep bool
	// Fees 是成员模拟账本的手续费。
	Fees PoolFees
}

// PoolMember 是池的一个成员：引用已有策略定义，启用时固化其 DSL 与版本。
type PoolMember struct {
	ID         string
	StrategyID string
}

// PoolAllocation 是资金分配方式。
type PoolAllocation struct {
	Method string
	// Ratios 是 fixed 用的固定比例（成员 id → 比例），可以不归一，总敞口受 portfolio.leverage 约束。
	Ratios   map[string]quant.Decimal
	Rotation *PoolRotation
}

// PoolRotation 是 rotation 的参数：多个因子在成员之间排名，按因子权重加权，取前 Top 名（并列的平分）平分 1。
type PoolRotation struct {
	Factors []RotationFactor
	Top     int
}

// RotationFactor 是一个轮动因子。
type RotationFactor struct {
	Name   string
	Window int
	Weight quant.Decimal
	// Order 是排序方向（asc、desc）；没写时 return 与 ma_ratio 取 desc，drawdown 取 asc。
	Order string
}

// PoolTiming 是择时参数。
type PoolTiming struct {
	Window int
}

// PoolFees 是成员账本的单边手续费（基点）。
type PoolFees struct {
	SpotBps float64
	SwapBps float64
}

// IsPool 报告策略是策略池。
func (s Strategy) IsPool() bool { return s.Pool != nil }

// MemberIDs 返回成员 id（按定义顺序）。
func (p StrategyPool) MemberIDs() []string {
	ids := make([]string, 0, len(p.Members))
	for _, member := range p.Members {
		ids = append(ids, member.ID)
	}
	return ids
}

// MaxWindow 返回轮动因子与择时里最长的窗口（都没有时为 0）。
func (p StrategyPool) MaxWindow() int {
	longest := 0
	if p.Allocation.Rotation != nil {
		for _, factor := range p.Allocation.Rotation.Factors {
			longest = max(longest, factor.Window)
		}
	}
	if p.Timing != nil {
		longest = max(longest, p.Timing.Window)
	}
	return longest
}

// MaxHoldBars 返回策略里最长的持有期：rank 规则的 holding.bars，tranche 子策略的 hold_period；没有持有期时为 0。
func (s Strategy) MaxHoldBars() int {
	longest := 0
	for _, rule := range s.Rules {
		if rule.Holding != nil {
			longest = max(longest, rule.Holding.Bars)
		}
		if rule.Tranche != nil {
			for _, sub := range rule.Tranche.Subs {
				longest = max(longest, sub.HoldPeriod)
			}
		}
	}
	return longest
}

// PoolWarmBars 是策略池预热需要推进的期数：轮动因子与择时里最长的窗口、成员里最长的持有期，取大者再加 1。
// 窗口决定成员净值序列要读满多少期；持有期决定长持有期的成员从空仓起步时前段净值的偏差要多久才消化，所以也要算进去。
func PoolWarmBars(pool Strategy, members []Strategy) int {
	longest := pool.Pool.MaxWindow()
	for _, member := range members {
		longest = max(longest, member.MaxHoldBars())
	}
	return longest + 1
}

// EffectiveOrder 返回因子的排序方向：写了用写的，没写按因子取默认（drawdown 越小越好，其余越大越好）。
func (f RotationFactor) EffectiveOrder() string {
	if f.Order != "" {
		return f.Order
	}
	if f.Name == FactorDrawdown {
		return OrderAsc
	}
	return OrderDesc
}

func decodeStrategyPool(node *yaml.Node) (*StrategyPool, error) {
	fields, err := mappingFields(node, "strategy_pool")
	if err != nil {
		return nil, err
	}
	pool := &StrategyPool{Fees: PoolFees{SpotBps: DefaultPoolSpotFeeBps, SwapBps: DefaultPoolSwapFeeBps}}
	for _, f := range fields {
		path := "strategy_pool." + f.key
		switch f.key {
		case "members":
			pool.Members, err = decodePoolMembers(f.value, path)
		case "allocation":
			pool.Allocation, err = decodePoolAllocation(f.value, path)
		case "timing":
			pool.Timing, err = decodePoolTiming(f.value, path)
		case "step":
			pool.Step, err = scalarDecimal(f.value, path)
			pool.HasStep = err == nil
		case "fees":
			pool.Fees, err = decodePoolFees(f.value, path)
		default:
			return nil, fmt.Errorf("strategy_pool 包含未知字段 %q", f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return pool, nil
}

func decodePoolMembers(node *yaml.Node, path string) ([]PoolMember, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是成员列表，每个成员写 id 与 strategy", path)
	}
	members := make([]PoolMember, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		fields, err := mappingFields(item, itemPath)
		if err != nil {
			return nil, err
		}
		var member PoolMember
		for _, f := range fields {
			switch f.key {
			case "id":
				member.ID, err = scalarRequiredText(f.value, itemPath+".id")
			case "strategy":
				member.StrategyID, err = scalarRequiredText(f.value, itemPath+".strategy")
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", itemPath, f.key)
			}
			if err != nil {
				return nil, err
			}
		}
		members = append(members, member)
	}
	return members, nil
}

func decodePoolAllocation(node *yaml.Node, path string) (PoolAllocation, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return PoolAllocation{}, err
	}
	var allocation PoolAllocation
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "method":
			allocation.Method, err = scalarRequiredText(f.value, fieldPath)
		case "ratios":
			allocation.Ratios, err = decodePoolRatios(f.value, fieldPath)
		case "rotation":
			allocation.Rotation, err = decodePoolRotation(f.value, fieldPath)
		default:
			return PoolAllocation{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return PoolAllocation{}, err
		}
	}
	return allocation, nil
}

func decodePoolRatios(node *yaml.Node, path string) (map[string]quant.Decimal, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	ratios := make(map[string]quant.Decimal, len(fields))
	for _, f := range fields {
		ratios[f.key], err = scalarDecimal(f.value, path+"."+f.key)
		if err != nil {
			return nil, err
		}
	}
	return ratios, nil
}

func decodePoolRotation(node *yaml.Node, path string) (*PoolRotation, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	rotation := &PoolRotation{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "factors":
			rotation.Factors, err = decodeRotationFactors(f.value, fieldPath)
		case "top":
			rotation.Top, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return rotation, nil
}

func decodeRotationFactors(node *yaml.Node, path string) ([]RotationFactor, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是因子列表，每个因子写 name、window，可选 weight、order", path)
	}
	factors := make([]RotationFactor, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		fields, err := mappingFields(item, itemPath)
		if err != nil {
			return nil, err
		}
		factor := RotationFactor{Weight: quant.One()}
		for _, f := range fields {
			switch f.key {
			case "name":
				factor.Name, err = scalarRequiredText(f.value, itemPath+".name")
			case "window":
				factor.Window, err = scalarInt(f.value, itemPath+".window")
			case "weight":
				factor.Weight, err = scalarDecimal(f.value, itemPath+".weight")
			case "order":
				factor.Order, err = scalarRequiredText(f.value, itemPath+".order")
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", itemPath, f.key)
			}
			if err != nil {
				return nil, err
			}
		}
		factors = append(factors, factor)
	}
	return factors, nil
}

func decodePoolTiming(node *yaml.Node, path string) (*PoolTiming, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	timing := &PoolTiming{}
	for _, f := range fields {
		if f.key != "window" {
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if timing.Window, err = scalarInt(f.value, path+".window"); err != nil {
			return nil, err
		}
	}
	return timing, nil
}

func decodePoolFees(node *yaml.Node, path string) (PoolFees, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return PoolFees{}, err
	}
	fees := PoolFees{SpotBps: DefaultPoolSpotFeeBps, SwapBps: DefaultPoolSwapFeeBps}
	for _, f := range fields {
		var value quant.Decimal
		switch f.key {
		case "spot_bps", "swap_bps":
			value, err = scalarDecimal(f.value, path+"."+f.key)
		default:
			return PoolFees{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return PoolFees{}, err
		}
		bps, _ := strconv.ParseFloat(value.String(), 64)
		if f.key == "spot_bps" {
			fees.SpotBps = bps
		} else {
			fees.SwapBps = bps
		}
	}
	return fees, nil
}

// validatePool 校验策略池自身的结构（不查成员：成员存在与否、成员的市场与周期由 ValidatePoolMembers 与启用时的解析检查）。
func validatePool(s *Strategy) error {
	if len(s.Rules) > 0 {
		return errors.New("rules 与 strategy_pool 只能写一个：策略池的逻辑都在成员策略里")
	}
	if s.MarketDeclared {
		return errors.New("策略池不写 market：成员各自显式写 market，池要绑定的因子任务是成员所需市场的并集")
	}
	if s.Bar == "" {
		return errors.New("策略池必须写 bar：成员的周期要与池一致")
	}
	if len(s.FactorParams) > 0 {
		return errors.New("策略池不支持 factor_params：多个成员可能给同一个可变成员写参数，会冲突")
	}
	if len(s.Universe.Tags)+len(s.Universe.ExcludeTags)+len(s.Universe.Include)+len(s.Universe.Exclude) > 0 || s.Universe.MinAgeBars != 0 || s.Universe.MaxAgeBars != 0 || s.Universe.AgeMarkets != "" {
		return errors.New("策略池不写 universe：标的范围由各成员自己的 universe 决定")
	}
	pool := s.Pool
	if len(pool.Members) == 0 {
		return errors.New("strategy_pool.members 至少需要一个成员")
	}
	if len(pool.Members) > MaxPoolMembers {
		return fmt.Errorf("strategy_pool.members 不能超过 %d 个", MaxPoolMembers)
	}
	ids := make(map[string]struct{}, len(pool.Members))
	for i := range pool.Members {
		member := &pool.Members[i]
		member.ID, member.StrategyID = strings.TrimSpace(member.ID), strings.TrimSpace(member.StrategyID)
		if !ruleIDPattern.MatchString(member.ID) {
			return fmt.Errorf("成员 id %q 必须是小写字母开头的 snake_case", member.ID)
		}
		if member.StrategyID == "" {
			return fmt.Errorf("成员 %s 缺少 strategy", member.ID)
		}
		if _, dup := ids[member.ID]; dup {
			return fmt.Errorf("成员 id %q 重复", member.ID)
		}
		ids[member.ID] = struct{}{}
	}
	if err := validatePoolAllocation(pool, ids); err != nil {
		return err
	}
	if pool.Timing != nil && (pool.Timing.Window < 1 || pool.Timing.Window > MaxPoolWindow) {
		return fmt.Errorf("strategy_pool.timing.window 必须在 1 到 %d 之间", MaxPoolWindow)
	}
	if pool.HasStep && (pool.Step.IsZero() || pool.Step.IsNegative() || pool.Step.Cmp(MaxLeverage) > 0) {
		return fmt.Errorf("strategy_pool.step 必须大于 0 且不超过 %s", MaxLeverage.String())
	}
	for name, fee := range map[string]float64{"spot_bps": pool.Fees.SpotBps, "swap_bps": pool.Fees.SwapBps} {
		if fee < 0 || fee > MaxPoolFeeBps {
			return fmt.Errorf("strategy_pool.fees.%s 必须在 0 到 %d 之间", name, MaxPoolFeeBps)
		}
	}
	return nil
}

func validatePoolAllocation(pool *StrategyPool, ids map[string]struct{}) error {
	allocation := &pool.Allocation
	switch allocation.Method {
	case AllocationFixed:
		if allocation.Rotation != nil {
			return errors.New("strategy_pool.allocation.method 是 fixed，不能写 rotation")
		}
		if len(allocation.Ratios) == 0 {
			return errors.New("strategy_pool.allocation.method 是 fixed，需要写 ratios（成员 id → 比例）")
		}
		names := make([]string, 0, len(allocation.Ratios))
		for id := range allocation.Ratios {
			names = append(names, id)
		}
		sort.Strings(names)
		for _, id := range names {
			if _, ok := ids[id]; !ok {
				return fmt.Errorf("allocation.ratios 里的 %q 不是池的成员", id)
			}
			if ratio := allocation.Ratios[id]; ratio.IsNegative() || ratio.Cmp(MaxLeverage) > 0 {
				return fmt.Errorf("allocation.ratios.%s 必须在 0 到 %s 之间", id, MaxLeverage.String())
			}
		}
		for _, member := range pool.Members {
			if _, ok := allocation.Ratios[member.ID]; !ok {
				return fmt.Errorf("allocation.ratios 缺少成员 %s 的比例（不分配请写 0）", member.ID)
			}
		}
	case AllocationRotation:
		if len(allocation.Ratios) > 0 {
			return errors.New("strategy_pool.allocation.method 是 rotation，不能写 ratios")
		}
		return validatePoolRotation(allocation.Rotation, len(pool.Members))
	case "":
		return errors.New("strategy_pool.allocation.method 必须写 fixed 或 rotation")
	default:
		return fmt.Errorf("strategy_pool.allocation.method %q 不受支持，只能是 fixed 或 rotation", allocation.Method)
	}
	return nil
}

func validatePoolRotation(rotation *PoolRotation, members int) error {
	if rotation == nil || len(rotation.Factors) == 0 {
		return errors.New("strategy_pool.allocation.method 是 rotation，需要写 rotation.factors")
	}
	if rotation.Top < 1 || rotation.Top > members {
		return fmt.Errorf("rotation.top 必须在 1 到成员数 %d 之间", members)
	}
	for i := range rotation.Factors {
		factor := &rotation.Factors[i]
		prefix := fmt.Sprintf("rotation.factors[%d]", i)
		switch factor.Name {
		case FactorReturn, FactorMARatio, FactorDrawdown:
		default:
			return fmt.Errorf("%s 的 name %q 不受支持，可选：%s、%s、%s", prefix, factor.Name, FactorReturn, FactorMARatio, FactorDrawdown)
		}
		if factor.Window < 1 || factor.Window > MaxPoolWindow {
			return fmt.Errorf("%s 的 window 必须在 1 到 %d 之间", prefix, MaxPoolWindow)
		}
		if factor.Weight.IsZero() || factor.Weight.IsNegative() {
			return fmt.Errorf("%s 的 weight 必须大于 0", prefix)
		}
		if factor.Order != "" && factor.Order != OrderAsc && factor.Order != OrderDesc {
			return fmt.Errorf("%s 的 order 只能是 asc 或 desc", prefix)
		}
	}
	return nil
}

// ValidatePoolMember 校验一个成员策略能不能进池：不能是池（不嵌套）、必须显式写 market（股票策略不允许写 market，所以不能进池）、
// 不能用 factor_params、写了 bar 的要与池一致。
func ValidatePoolMember(pool Strategy, id string, member Strategy) error {
	switch {
	case member.IsPool():
		return fmt.Errorf("成员 %s 引用的策略本身是策略池，策略池不能嵌套", id)
	case !member.MarketDeclared:
		return fmt.Errorf("成员 %s 引用的策略没有显式写 market：池成员必须写 market（股票策略不能进池）", id)
	case len(member.FactorParams) > 0:
		return fmt.Errorf("成员 %s 引用的策略用了 factor_params：多个成员可能给同一个可变成员写参数，池成员不能用", id)
	case member.Bar != "" && member.Bar != pool.Bar:
		return fmt.Errorf("成员 %s 的 bar 是 %s，与池的 bar %s 不一致", id, member.Bar, pool.Bar)
	}
	return nil
}

// UnionMarket 返回策略池的有效市场模式：成员市场的并集。只涉及一个市场时就是该市场的单市场模式，涉及现货与永续时是
// select=both、prefer=spot（只用来表示“两个市场都涉及”：决定要绑定的因子任务、账户要求的可交易市场与组合约束的口径，
// 成员各自的选币与下单仍按成员自己的模式）。
func UnionMarket(modes []Market) Market {
	spot, swap := false, false
	for _, mode := range modes {
		spot = spot || mode.Has(MarketSpot)
		swap = swap || mode.Has(MarketSwap)
	}
	switch {
	case spot && swap:
		return Market{Select: MarketBoth, Prefer: MarketSpot}
	case swap:
		return SingleMarket(MarketSwap)
	default:
		return SingleMarket(MarketSpot)
	}
}

// ValidatePoolMembers 在成员定义都取到之后校验整个池：每个成员都能进池，池的 portfolio 与成员市场的并集相容。
// members 的键是成员 id，必须覆盖池的全部成员。
func ValidatePoolMembers(pool Strategy, members map[string]Strategy) (Market, error) {
	if !pool.IsPool() {
		return Market{}, errors.New("不是策略池")
	}
	modes := make([]Market, 0, len(pool.Pool.Members))
	for _, member := range pool.Pool.Members {
		strategy, ok := members[member.ID]
		if !ok {
			return Market{}, fmt.Errorf("成员 %s 引用的策略 %s 不存在", member.ID, member.StrategyID)
		}
		if err := ValidatePoolMember(pool, member.ID, strategy); err != nil {
			return Market{}, err
		}
		modes = append(modes, strategy.Market)
	}
	union := UnionMarket(modes)
	if !union.Has(MarketSwap) && pool.Portfolio.Leverage.Cmp(quant.One()) > 0 {
		return Market{}, fmt.Errorf("成员只涉及现货，不允许 portfolio.leverage 大于 1（当前 %s），加杠杆需要永续市场", pool.Portfolio.Leverage.String())
	}
	return union, nil
}
