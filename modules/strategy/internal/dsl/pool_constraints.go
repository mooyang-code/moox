package dsl

import (
	"errors"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// 策略池的组合约束：条件转移（allocation.switch）、净敞口区间（exposure）与总杠杆区间（gross）。三者都按成员汇总的分配比例计算，
// 要求成员是单方向的（规则全部做多或全部做空，见 Strategy.Direction）。

// 净敞口区间超出时的修正方式。
const (
	AdjustAuto         = "auto"          // 总敞口大于 pivot 时同 reduce_larger，否则同 raise_smaller
	AdjustReduceLarger = "reduce_larger" // 缩小超出方向的一侧：净敞口高于 max 时缩多头，低于 min 时缩空头
	AdjustRaiseSmaller = "raise_smaller" // 放大另一侧：净敞口高于 max 时放大空头，低于 min 时放大多头
)

// PoolSwitch 是一条条件转移：WhenEmpty 成员本期的有效目标为空时，把 From 的分配比例全部转给 To（按它们的比例分，都为 0 时平分）。
type PoolSwitch struct {
	WhenEmpty string
	From      string
	To        []string
}

// PoolExposure 是净敞口区间：各段按书写顺序执行。
type PoolExposure struct {
	Bands []ExposureBand
}

// ExposureBand 是一段净敞口区间 [Min, Max]。HasBelow、HasAbove 为真时只在总敞口小于 WhenGrossBelow（大于 WhenGrossAbove）时执行；
// Pivot 只在 Adjust 为 auto 时使用。
type ExposureBand struct {
	Min, Max                       float64
	Adjust                         string
	Pivot                          float64
	WhenGrossBelow, WhenGrossAbove float64
	HasBelow, HasAbove             bool
}

// PoolGross 是总杠杆区间：大于 Max 时整体缩小，小于 Min 时整体放大（池的第一根不放大）。
type PoolGross struct {
	Min, Max       float64
	HasMin, HasMax bool
}

// NeedsSingleSide 报告池写了要求成员单方向的字段（switch、exposure、gross）。
func (p StrategyPool) NeedsSingleSide() bool {
	return len(p.Allocation.Switch) > 0 || p.Exposure != nil || p.Gross != nil
}

// HasConstraints 报告池写了净敞口或总杠杆区间。
func (p StrategyPool) HasConstraints() bool { return p.Exposure != nil || p.Gross != nil }

// Direction 返回策略的方向：规则全部做多为 long，全部做空为 short；混合方向或没有规则时为空串。
// 规则的 side 默认 long；tranche 规则按子策略的 long_weight、short_weight 判断（它的 side 被固定写成 long）。
func (s Strategy) Direction() string {
	long, short := false, false
	for _, rule := range s.Rules {
		if rule.Tranche != nil {
			for _, sub := range rule.Tranche.Subs {
				long = long || sub.LongWeight > 0
				short = short || sub.ShortWeight > 0
			}
			continue
		}
		if rule.Side == SideShort {
			short = true
		} else {
			long = true
		}
	}
	switch {
	case long && !short:
		return SideLong
	case short && !long:
		return SideShort
	}
	return ""
}

func decodePoolSwitch(node *yaml.Node, path string) ([]PoolSwitch, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须是列表，每项写 when: {empty: 成员 id} 与 move: {from: 成员 id, to: [成员 id…]}", path)
	}
	switches := make([]PoolSwitch, 0, len(node.Content))
	for i, item := range node.Content {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		fields, err := mappingFields(item, itemPath)
		if err != nil {
			return nil, err
		}
		var entry PoolSwitch
		for _, f := range fields {
			switch f.key {
			case "when":
				entry.WhenEmpty, err = decodeSwitchWhen(f.value, itemPath+".when")
			case "move":
				entry.From, entry.To, err = decodeSwitchMove(f.value, itemPath+".move")
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", itemPath, f.key)
			}
			if err != nil {
				return nil, err
			}
		}
		switches = append(switches, entry)
	}
	return switches, nil
}

func decodeSwitchWhen(node *yaml.Node, path string) (string, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return "", err
	}
	empty := ""
	for _, f := range fields {
		if f.key != "empty" {
			return "", fmt.Errorf("%s 包含未知字段 %q（条件第一版只有 empty）", path, f.key)
		}
		if empty, err = scalarRequiredText(f.value, path+".empty"); err != nil {
			return "", err
		}
	}
	return empty, nil
}

func decodeSwitchMove(node *yaml.Node, path string) (string, []string, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return "", nil, err
	}
	from := ""
	var to []string
	for _, f := range fields {
		switch f.key {
		case "from":
			from, err = scalarRequiredText(f.value, path+".from")
		case "to":
			to, err = stringList(f.value, path+".to")
		default:
			return "", nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return "", nil, err
		}
	}
	return from, to, nil
}

func decodePoolExposure(node *yaml.Node, path string) (*PoolExposure, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	exposure := &PoolExposure{}
	for _, f := range fields {
		if f.key != "bands" {
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		bandsPath := path + ".bands"
		if f.value == nil || f.value.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s 必须是区间列表，每段写 min、max、adjust", bandsPath)
		}
		for i, item := range f.value.Content {
			band, err := decodeExposureBand(item, fmt.Sprintf("%s[%d]", bandsPath, i))
			if err != nil {
				return nil, err
			}
			exposure.Bands = append(exposure.Bands, band)
		}
	}
	return exposure, nil
}

func decodeExposureBand(node *yaml.Node, path string) (ExposureBand, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return ExposureBand{}, err
	}
	band := ExposureBand{}
	hasMin, hasMax, hasPivot := false, false, false
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "min":
			band.Min, err = scalarNumber(f.value, fieldPath)
			hasMin = true
		case "max":
			band.Max, err = scalarNumber(f.value, fieldPath)
			hasMax = true
		case "adjust":
			band.Adjust, err = scalarRequiredText(f.value, fieldPath)
		case "pivot":
			band.Pivot, err = scalarNumber(f.value, fieldPath)
			hasPivot = true
		case "when_gross_below":
			band.WhenGrossBelow, err = scalarNumber(f.value, fieldPath)
			band.HasBelow = true
		case "when_gross_above":
			band.WhenGrossAbove, err = scalarNumber(f.value, fieldPath)
			band.HasAbove = true
		default:
			return ExposureBand{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return ExposureBand{}, err
		}
	}
	switch {
	case !hasMin || !hasMax:
		return ExposureBand{}, fmt.Errorf("%s 必须同时写 min 与 max", path)
	case band.Min > band.Max:
		return ExposureBand{}, fmt.Errorf("%s 的 min 不能大于 max", path)
	}
	switch band.Adjust {
	case AdjustAuto:
		if !hasPivot {
			return ExposureBand{}, fmt.Errorf("%s 的 adjust 是 auto，需要写 pivot（总敞口大于它时缩小，否则放大）", path)
		}
	case AdjustReduceLarger, AdjustRaiseSmaller:
		if hasPivot {
			return ExposureBand{}, fmt.Errorf("%s 的 pivot 只能与 adjust: auto 一起写", path)
		}
	default:
		return ExposureBand{}, fmt.Errorf("%s 的 adjust 只能是 auto、reduce_larger 或 raise_smaller", path)
	}
	return band, nil
}

func decodePoolGross(node *yaml.Node, path string) (*PoolGross, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	gross := &PoolGross{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "min":
			gross.Min, err = scalarNumber(f.value, fieldPath)
			gross.HasMin = true
		case "max":
			gross.Max, err = scalarNumber(f.value, fieldPath)
			gross.HasMax = true
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return gross, nil
}

// scalarNumber 解析一个十进制数并转为 float64（组合约束都在比例的 float64 副本上计算）。
func scalarNumber(node *yaml.Node, path string) (float64, error) {
	value, err := scalarDecimal(node, path)
	if err != nil {
		return 0, err
	}
	number, err := strconv.ParseFloat(value.String(), 64)
	if err != nil {
		return 0, fmt.Errorf("%s 不是有效的数：%w", path, err)
	}
	return number, nil
}

// validatePoolConstraints 校验组合约束的结构（成员方向在 ValidatePoolMembers 里检查）。leverage 是 portfolio.leverage。
func validatePoolConstraints(pool *StrategyPool, ids map[string]struct{}, leverage float64) error {
	if len(pool.Allocation.Switch) > 0 && pool.Allocation.Method != AllocationFixed {
		return fmt.Errorf("strategy_pool.allocation.switch 只能与 method: fixed 一起写（rotation 不支持）")
	}
	for i, entry := range pool.Allocation.Switch {
		prefix := fmt.Sprintf("allocation.switch[%d]", i)
		if entry.WhenEmpty == "" {
			return fmt.Errorf("%s 需要写 when: {empty: 成员 id}", prefix)
		}
		if _, ok := ids[entry.WhenEmpty]; !ok {
			return fmt.Errorf("%s 的 when.empty %q 不是池的成员", prefix, entry.WhenEmpty)
		}
		if entry.From == "" || len(entry.To) == 0 {
			return fmt.Errorf("%s 需要写 move: {from: 成员 id, to: [成员 id…]}", prefix)
		}
		if _, ok := ids[entry.From]; !ok {
			return fmt.Errorf("%s 的 move.from %q 不是池的成员", prefix, entry.From)
		}
		for _, to := range entry.To {
			if _, ok := ids[to]; !ok {
				return fmt.Errorf("%s 的 move.to 里的 %q 不是池的成员", prefix, to)
			}
			if to == entry.From {
				return fmt.Errorf("%s 的 move.to 不能包含 from 自己（%s）", prefix, to)
			}
		}
	}
	if pool.Exposure != nil && len(pool.Exposure.Bands) == 0 {
		return errors.New("strategy_pool.exposure.bands 至少需要一段")
	}
	if gross := pool.Gross; gross != nil {
		switch {
		case !gross.HasMin && !gross.HasMax:
			return errors.New("strategy_pool.gross 至少写 min、max 之一")
		case gross.HasMin && gross.Min < 0:
			return errors.New("strategy_pool.gross.min 不能为负数")
		case gross.HasMax && gross.Max <= 0:
			return errors.New("strategy_pool.gross.max 必须大于 0")
		case gross.HasMin && gross.HasMax && gross.Min > gross.Max:
			return errors.New("strategy_pool.gross.min 不能大于 max")
		case gross.HasMax && gross.Max > leverage:
			return errors.New("strategy_pool.gross.max 不能超过 portfolio.leverage")
		case gross.HasMin && gross.Min > leverage:
			return errors.New("strategy_pool.gross.min 不能超过 portfolio.leverage")
		}
	}
	return nil
}

// validatePoolSides 在成员定义取到之后检查方向：写了 switch、exposure、gross 时每个成员都必须是单方向的，switch 的 from 与 to 必须同方向。
func validatePoolSides(pool StrategyPool, members map[string]Strategy) error {
	if !pool.NeedsSingleSide() {
		return nil
	}
	sides := make(map[string]string, len(pool.Members))
	for _, member := range pool.Members {
		side := members[member.ID].Direction()
		if side == "" {
			return fmt.Errorf("成员 %s 同时有多头与空头规则：写了 allocation.switch、exposure 或 gross 时，每个成员都必须是单方向的（规则全部做多或全部做空）", member.ID)
		}
		sides[member.ID] = side
	}
	for i, entry := range pool.Allocation.Switch {
		for _, to := range entry.To {
			if sides[to] != sides[entry.From] {
				return fmt.Errorf("allocation.switch[%d] 只能在同方向的成员之间转移：from %s 是%s，to 里的 %s 是%s；跨方向会改变净敞口，请用 exposure", i, entry.From, sideLabel(sides[entry.From]), to, sideLabel(sides[to]))
			}
		}
	}
	return nil
}

func sideLabel(side string) string {
	if side == SideShort {
		return "空头"
	}
	return "多头"
}

// MemberSides 返回各成员的方向（成员 id → long、short；混合方向的成员为空串），池的组合约束按它汇总多头与空头敞口。
func MemberSides(pool StrategyPool, members map[string]Strategy) map[string]string {
	sides := make(map[string]string, len(pool.Members))
	for _, member := range pool.Members {
		if strategy, ok := members[member.ID]; ok {
			sides[member.ID] = strategy.Direction()
		}
	}
	return sides
}
