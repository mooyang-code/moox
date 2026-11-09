package dsl

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// 策略池的漂移分配（allocation.method: drift）：成员的分配系数随成员持仓价值的自然变化漂移，再按两侧各自的规则拉回目标比例；
// 另有波动冻结（strategy_pool.freeze）与漂移起点（allocation.start、allocation.initial）。都只用于 drift，要求成员是单方向的。

const (
	// AllocationDrift 让分配系数随成员持仓的名义价值漂移，再按 allocation.sides 侧内再平衡。
	AllocationDrift = "drift"

	// 侧内再平衡的方式。
	RebalanceNone      = "none"      // 不再平衡（默认）
	RebalanceEveryBar  = "every_bar" // 每根拉回目标构成
	RebalancePeriodic  = "periodic"  // 从 anchor 起每 period_bars 根触发一次
	RebalanceThreshold = "threshold" // 任一成员的侧内占比相对目标偏离超过 threshold 时触发
	RebalanceBoth      = "both"      // periodic 与 threshold 都开

	// 侧内构成的漂移口径。
	DriftByNotional = "notional" // 按持仓名义价值漂移（默认）
	DriftByNAV      = "nav"      // 按成员资金收益漂移，盈利的成员占比变大
)

// PoolSide 是 drift 一侧（多头或空头）的侧内再平衡配置。
type PoolSide struct {
	Rebalance string
	// PeriodBars 与 Anchor 只用于 periodic、both；Anchor 是相位起点（某根 bar 的 bar_start 或交易日期），没写时取漂移起点。
	PeriodBars int
	Anchor     string
	// Threshold 只用于 threshold、both：|侧内占比 ÷ 目标侧内占比 − 1| 超过它时触发。
	Threshold float64
	// SpreadBars 是触发后分几根走完（1 表示一次到位）。
	SpreadBars int
	DriftBy    string
}

// Periodic、ByThreshold 报告这一侧是否按周期、按偏离触发。
func (s PoolSide) Periodic() bool {
	return s.Rebalance == RebalancePeriodic || s.Rebalance == RebalanceBoth
}
func (s PoolSide) ByThreshold() bool {
	return s.Rebalance == RebalanceThreshold || s.Rebalance == RebalanceBoth
}

// PoolInitial 是 allocation.initial：漂移从 At 这一根开始，分配系数在这一根置为 Ratios。
type PoolInitial struct {
	At     string
	Ratios map[string]quant.Decimal
}

// PoolFreeze 是波动冻结：被观察成员按 ratios 加权的净值在 WindowBars 根内的涨跌幅绝对值超过 Threshold 时，
// 从触发那根起冻结 Bars 根（跳过侧内再平衡、净敞口与总杠杆区间）。WatchSide 与 WatchMembers 只写一个。
type PoolFreeze struct {
	WatchSide    string
	WatchMembers []string
	WindowBars   int
	Threshold    float64
	Bars         int
}

// Side 返回 drift 一侧的配置：没写时不再平衡、按名义价值漂移、一次到位。
func (p StrategyPool) Side(side string) PoolSide {
	if config, ok := p.Allocation.Sides[side]; ok && config != nil {
		return *config
	}
	return PoolSide{Rebalance: RebalanceNone, SpreadBars: 1, DriftBy: DriftByNotional}
}

// DriftStart 返回漂移起点的时刻文本（allocation.start 或 allocation.initial.at），都没写时为空。
func (a PoolAllocation) DriftStart() string {
	if a.Initial != nil {
		return a.Initial.At
	}
	return a.Start
}

// StartRatios 返回漂移起点的分配系数：写了 initial 时是 initial.ratios，否则是 ratios。
func (a PoolAllocation) StartRatios() map[string]quant.Decimal {
	if a.Initial != nil {
		return a.Initial.Ratios
	}
	return a.Ratios
}

func decodePoolSides(node *yaml.Node, path string) (map[string]*PoolSide, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	sides := make(map[string]*PoolSide, len(fields))
	for _, f := range fields {
		if f.key != SideLong && f.key != SideShort {
			return nil, fmt.Errorf("%s 只能写 long 与 short，不认识 %q", path, f.key)
		}
		side, err := decodePoolSide(f.value, path+"."+f.key)
		if err != nil {
			return nil, err
		}
		sides[f.key] = side
	}
	return sides, nil
}

func decodePoolSide(node *yaml.Node, path string) (*PoolSide, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	side := &PoolSide{Rebalance: RebalanceNone, SpreadBars: 1, DriftBy: DriftByNotional}
	spread := false
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "rebalance":
			side.Rebalance, err = scalarRequiredText(f.value, fieldPath)
		case "period_bars":
			side.PeriodBars, err = scalarInt(f.value, fieldPath)
		case "anchor":
			side.Anchor, err = scalarRequiredText(f.value, fieldPath)
		case "threshold":
			side.Threshold, err = scalarNumber(f.value, fieldPath)
		case "spread_bars":
			side.SpreadBars, err = scalarInt(f.value, fieldPath)
			spread = true
		case "drift_by":
			side.DriftBy, err = scalarRequiredText(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if spread && side.Rebalance == RebalanceNone {
		return nil, fmt.Errorf("%s.spread_bars 只能与会触发再平衡的 rebalance 一起写（当前是 none）", path)
	}
	return side, nil
}

func decodePoolInitial(node *yaml.Node, path string) (*PoolInitial, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	initial := &PoolInitial{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "at":
			initial.At, err = scalarRequiredText(f.value, fieldPath)
		case "ratios":
			initial.Ratios, err = decodePoolRatios(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return initial, nil
}

func decodePoolFreeze(node *yaml.Node, path string) (*PoolFreeze, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	freeze := &PoolFreeze{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "watch":
			if f.value != nil && f.value.Kind == yaml.SequenceNode {
				freeze.WatchMembers, err = stringList(f.value, fieldPath)
			} else {
				freeze.WatchSide, err = scalarRequiredText(f.value, fieldPath)
			}
		case "window_bars":
			freeze.WindowBars, err = scalarInt(f.value, fieldPath)
		case "threshold":
			freeze.Threshold, err = scalarNumber(f.value, fieldPath)
		case "bars":
			freeze.Bars, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return freeze, nil
}

// validatePoolRatios 校验一组成员比例：必须覆盖所有成员、只含池的成员，每个比例在 0 到 MaxLeverage 之间。path 是字段路径（如 allocation.ratios）。
func validatePoolRatios(path string, ratios map[string]quant.Decimal, pool *StrategyPool, ids map[string]struct{}) error {
	names := make([]string, 0, len(ratios))
	for id := range ratios {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		if _, ok := ids[id]; !ok {
			return fmt.Errorf("%s 里的 %q 不是池的成员", path, id)
		}
		if ratio := ratios[id]; ratio.IsNegative() || ratio.Cmp(MaxLeverage) > 0 {
			return fmt.Errorf("%s.%s 必须在 0 到 %s 之间", path, id, MaxLeverage.String())
		}
	}
	for _, member := range pool.Members {
		if _, ok := ratios[member.ID]; !ok {
			return fmt.Errorf("%s 缺少成员 %s 的比例（不分配请写 0）", path, member.ID)
		}
	}
	return nil
}

// validatePoolDrift 校验只属于 drift 的字段：method 不是 drift 时一律不能写；是 drift 时校验 sides、start/initial 与 freeze，
// 并拒绝 timing、step（它们是 fixed、rotation 调整比例的方式）。
func validatePoolDrift(pool *StrategyPool, ids map[string]struct{}) error {
	allocation := &pool.Allocation
	if allocation.Method != AllocationDrift {
		switch {
		case allocation.Sides != nil:
			return errors.New("strategy_pool.allocation.sides 只能与 method: drift 一起写")
		case allocation.Start != "" || allocation.Initial != nil:
			return errors.New("strategy_pool.allocation.start 与 initial 是漂移起点，只能与 method: drift 一起写")
		case pool.Freeze != nil:
			return errors.New("strategy_pool.freeze 只能与 allocation.method: drift 一起写")
		}
		return nil
	}
	switch {
	case pool.Timing != nil:
		return errors.New("strategy_pool.allocation.method 是 drift，不能写 timing：择时是 fixed、rotation 调整比例的方式")
	case pool.HasStep:
		return errors.New("strategy_pool.allocation.method 是 drift，不能写 step：步长是 fixed、rotation 调整比例的方式")
	case allocation.Start != "" && allocation.Initial != nil:
		return errors.New("strategy_pool.allocation.start 与 initial 只能写一个")
	}
	if allocation.Start != "" {
		if _, err := ParseAnchor(allocation.Start); err != nil {
			return fmt.Errorf("strategy_pool.allocation.start %s", err)
		}
	}
	if initial := allocation.Initial; initial != nil {
		if initial.At == "" || initial.Ratios == nil {
			return errors.New("strategy_pool.allocation.initial 需要同时写 at（起点时刻）与 ratios（这一刻各成员的比例）")
		}
		if _, err := ParseAnchor(initial.At); err != nil {
			return fmt.Errorf("strategy_pool.allocation.initial.at %s", err)
		}
		if err := validatePoolRatios("allocation.initial.ratios", initial.Ratios, pool, ids); err != nil {
			return err
		}
	}
	for _, name := range []string{SideLong, SideShort} {
		side, ok := allocation.Sides[name]
		if !ok {
			continue
		}
		if err := validatePoolSide("strategy_pool.allocation.sides."+name, side); err != nil {
			return err
		}
	}
	if pool.Freeze != nil {
		return validatePoolFreeze(pool.Freeze, ids)
	}
	return nil
}

func validatePoolSide(path string, side *PoolSide) error {
	switch side.Rebalance {
	case RebalanceNone, RebalanceEveryBar, RebalancePeriodic, RebalanceThreshold, RebalanceBoth:
	default:
		return fmt.Errorf("%s.rebalance 只能是 none、every_bar、periodic、threshold 或 both", path)
	}
	switch side.DriftBy {
	case DriftByNotional, DriftByNAV:
	default:
		return fmt.Errorf("%s.drift_by 只能是 notional 或 nav", path)
	}
	if side.Periodic() {
		if side.PeriodBars < 1 || side.PeriodBars > MaxCount {
			return fmt.Errorf("%s.period_bars 必须在 1 到 %d 之间（rebalance 是 %s 时必填）", path, MaxCount, side.Rebalance)
		}
		if side.Anchor != "" {
			if _, err := ParseAnchor(side.Anchor); err != nil {
				return fmt.Errorf("%s.anchor %s", path, err)
			}
		}
	} else if side.PeriodBars != 0 || side.Anchor != "" {
		return fmt.Errorf("%s 的 period_bars、anchor 只能与 rebalance: periodic 或 both 一起写", path)
	}
	if side.ByThreshold() {
		if !(side.Threshold > 0) || math.IsInf(side.Threshold, 0) {
			return fmt.Errorf("%s.threshold 必须大于 0（rebalance 是 %s 时必填）", path, side.Rebalance)
		}
	} else if side.Threshold != 0 {
		return fmt.Errorf("%s.threshold 只能与 rebalance: threshold 或 both 一起写", path)
	}
	if side.SpreadBars < 1 || side.SpreadBars > MaxCount {
		return fmt.Errorf("%s.spread_bars 必须在 1 到 %d 之间", path, MaxCount)
	}
	return nil
}

func validatePoolFreeze(freeze *PoolFreeze, ids map[string]struct{}) error {
	switch {
	case freeze.WatchSide == "" && len(freeze.WatchMembers) == 0:
		return errors.New("strategy_pool.freeze.watch 必须写 long、short 或成员 id 列表")
	case freeze.WatchSide != "" && freeze.WatchSide != SideLong && freeze.WatchSide != SideShort:
		return fmt.Errorf("strategy_pool.freeze.watch %q 只能是 long、short 或成员 id 列表", freeze.WatchSide)
	}
	seen := make(map[string]struct{}, len(freeze.WatchMembers))
	for _, id := range freeze.WatchMembers {
		if _, ok := ids[id]; !ok {
			return fmt.Errorf("strategy_pool.freeze.watch 里的 %q 不是池的成员", id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("strategy_pool.freeze.watch 里的 %q 重复", id)
		}
		seen[id] = struct{}{}
	}
	switch {
	case freeze.WindowBars < 1 || freeze.WindowBars > MaxPoolWindow:
		return fmt.Errorf("strategy_pool.freeze.window_bars 必须在 1 到 %d 之间", MaxPoolWindow)
	case !(freeze.Threshold > 0) || math.IsInf(freeze.Threshold, 0):
		return errors.New("strategy_pool.freeze.threshold 必须大于 0")
	case freeze.Bars < 1 || freeze.Bars > MaxCount:
		return fmt.Errorf("strategy_pool.freeze.bars 必须在 1 到 %d 之间", MaxCount)
	}
	return nil
}

// validateFreezeSides 在成员方向确定后检查 freeze.watch 写的一侧有成员。
func validateFreezeSides(pool StrategyPool, sides map[string]string) error {
	if pool.Freeze == nil || pool.Freeze.WatchSide == "" {
		return nil
	}
	for _, member := range pool.Members {
		if sides[member.ID] == pool.Freeze.WatchSide {
			return nil
		}
	}
	return fmt.Errorf("strategy_pool.freeze.watch 是 %s，但池里没有%s成员", pool.Freeze.WatchSide, sideLabel(pool.Freeze.WatchSide))
}
