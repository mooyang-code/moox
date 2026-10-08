package dsl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

var ruleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Validate 校验结构、字段取值和表达式语法；不需要知道 View 的列，列的存在性在 Compile 时检查。
// 校验会就地规范化 side 与 method 的默认值；portfolio 的默认值在解析时填入，显式写出的 0 按原值校验。
func Validate(s *Strategy) error {
	if s == nil {
		return errors.New("策略 DSL 为空")
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("策略 DSL 缺少 name")
	}
	if s.Bar != "" {
		bar, err := frequencypkg.Parse(s.Bar)
		if err != nil {
			return fmt.Errorf("bar %q 不是规范频率：%w", s.Bar, err)
		}
		if bar == frequencypkg.Week1 || bar == frequencypkg.Month1 || bar == frequencypkg.Second30 {
			return fmt.Errorf("bar %q 不受支持，只支持分钟、小时和日线", s.Bar)
		}
		s.Bar = string(bar)
	}
	if s.Universe.MinAgeBars < 0 {
		return errors.New("universe.min_age_bars 不能为负数")
	}
	if err := validatePortfolio(&s.Portfolio); err != nil {
		return err
	}
	if len(s.Rules) == 0 {
		return errors.New("策略 DSL 至少需要一条规则")
	}
	ids := make(map[string]struct{}, len(s.Rules))
	totalBudget := quant.Zero()
	for i := range s.Rules {
		rule := &s.Rules[i]
		if err := validateRule(rule); err != nil {
			return err
		}
		if _, exists := ids[rule.ID]; exists {
			return fmt.Errorf("规则 id %q 重复", rule.ID)
		}
		ids[rule.ID] = struct{}{}
		totalBudget = totalBudget.Add(rule.Weight.Total)
	}
	if totalBudget.Cmp(s.Portfolio.Leverage) > 0 {
		return fmt.Errorf("各规则 weight.total 之和 %s 超过 portfolio.leverage %s", totalBudget.String(), s.Portfolio.Leverage.String())
	}
	return nil
}

func validatePortfolio(p *Portfolio) error {
	if p.Leverage.IsZero() || p.Leverage.IsNegative() {
		return errors.New("portfolio.leverage 必须大于 0")
	}
	if p.HasMaxWeight {
		if p.MaxWeight.IsZero() || p.MaxWeight.IsNegative() {
			return errors.New("portfolio.max_weight 必须大于 0")
		}
		if p.MaxWeight.Cmp(p.Leverage) > 0 {
			return errors.New("portfolio.max_weight 不能超过 portfolio.leverage")
		}
	}
	if p.MinUniverse < 0 {
		return errors.New("portfolio.min_universe 不能为负数")
	}
	if p.MaxMissing.IsNegative() || p.MaxMissing.Cmp(quant.One()) > 0 {
		return errors.New("portfolio.max_missing 必须在 0 到 1 之间")
	}
	return nil
}

func validateRule(rule *Rule) error {
	rule.ID = strings.TrimSpace(rule.ID)
	if rule.ID == "" {
		return errors.New("每条规则都需要 id")
	}
	if !ruleIDPattern.MatchString(rule.ID) {
		return fmt.Errorf("规则 id %q 必须是小写字母开头的 snake_case", rule.ID)
	}
	prefix := "规则 " + rule.ID
	if rule.Side == "" {
		rule.Side = SideLong
	}
	if rule.Side != SideLong && rule.Side != SideShort {
		return fmt.Errorf("%s 的 side 必须是 long 或 short", prefix)
	}
	if err := validateWeight(prefix, &rule.Weight); err != nil {
		return err
	}
	if rule.Pool.Explicit && len(rule.Pool.Fixed) == 0 && len(rule.Pool.Tags) == 0 {
		return fmt.Errorf("%s 的 pool 不能为空", prefix)
	}
	switch rule.Type {
	case RuleTypeRank:
		return validateRankRule(prefix, rule)
	case RuleTypeSignal:
		return validateSignalRule(prefix, rule)
	case "":
		return fmt.Errorf("%s 缺少 type（rank 或 signal）", prefix)
	default:
		return fmt.Errorf("%s 的 type %q 不受支持，只能是 rank 或 signal", prefix, rule.Type)
	}
}

func validateWeight(prefix string, weight *Weight) error {
	if weight.Method == "" {
		weight.Method = WeightMethodEqual
	}
	if weight.Method != WeightMethodEqual && weight.Method != WeightMethodRank {
		return fmt.Errorf("%s 的 weight.method 必须是 equal 或 rank", prefix)
	}
	if weight.Total.IsZero() || weight.Total.IsNegative() {
		return fmt.Errorf("%s 需要大于 0 的 weight.total", prefix)
	}
	if weight.HasCap {
		if weight.Cap.IsZero() || weight.Cap.IsNegative() {
			return fmt.Errorf("%s 的 weight.cap 必须大于 0", prefix)
		}
		if weight.Cap.Cmp(weight.Total) > 0 {
			return fmt.Errorf("%s 的 weight.cap 不能超过 weight.total", prefix)
		}
	}
	return nil
}

func validateRankRule(prefix string, rule *Rule) error {
	if rule.Entry != "" || rule.Exit != "" {
		return fmt.Errorf("%s 是 rank 规则，不能使用 entry / exit", prefix)
	}
	if rule.Score == "" {
		return fmt.Errorf("%s 缺少 score", prefix)
	}
	if !rule.HasSelect {
		return fmt.Errorf("%s 缺少 select", prefix)
	}
	sel := rule.Select
	if (sel.Top > 0) == (sel.Bottom > 0) {
		return fmt.Errorf("%s 的 select 必须且只能设置 top 或 bottom 之一", prefix)
	}
	if sel.Top < 0 || sel.Bottom < 0 || sel.Buffer < 0 {
		return fmt.Errorf("%s 的 select.top、bottom、buffer 不能为负数", prefix)
	}
	// 能同时持有的标的数上限：普通规则是选中数量，holding 规则是各批次之和。
	slots := sel.Top
	if sel.Bottom > 0 {
		slots = sel.Bottom
	}
	if rule.Holding != nil {
		if sel.Buffer > 0 {
			return fmt.Errorf("%s 不能同时使用 holding 和 select.buffer", prefix)
		}
		if err := validateHolding(prefix, rule.Holding); err != nil {
			return err
		}
		slots *= len(rule.Holding.Offsets)
	}
	if rule.Weight.HasCap && rule.Weight.Cap.Mul(quant.Must(fmt.Sprint(slots))).Cmp(rule.Weight.Total) < 0 {
		return fmt.Errorf("%s 的 weight.cap 乘以最多可持有的标的数小于 weight.total，预算永远分不完", prefix)
	}
	return analyzeStages(prefix, rule.stageSources())
}

func validateSignalRule(prefix string, rule *Rule) error {
	if !rule.Pool.Explicit {
		return fmt.Errorf("%s 是 signal 规则，必须显式给出 pool", prefix)
	}
	if rule.Score != "" || rule.HasSelect || rule.Filter != "" || rule.FilterAfter != "" || rule.Holding != nil {
		return fmt.Errorf("%s 是 signal 规则，只能使用 pool、entry、exit、weight、side", prefix)
	}
	if rule.Entry == "" || rule.Exit == "" {
		return fmt.Errorf("%s 需要同时给出 entry 和 exit", prefix)
	}
	if rule.Weight.Method != WeightMethodEqual {
		return fmt.Errorf("%s 是 signal 规则，weight.method 只能是 equal", prefix)
	}
	if rule.Weight.HasCap {
		return fmt.Errorf("%s 是 signal 规则，不支持 weight.cap", prefix)
	}
	return analyzeStages(prefix, rule.stageSources())
}

// analyzeStages 按固定阶段顺序做语法检查，保证同一份 DSL 总是报告同一个错误。
func analyzeStages(prefix string, sources []stageSource) error {
	for _, item := range sources {
		if item.source == "" {
			continue
		}
		if _, err := Analyze(item.source, item.stage); err != nil {
			return fmt.Errorf("%s 的 %s 表达式无效：%w", prefix, item.stage, err)
		}
	}
	return nil
}

func validateHolding(prefix string, holding *Holding) error {
	if holding.Bars <= 0 {
		return fmt.Errorf("%s 的 holding.bars 必须大于 0", prefix)
	}
	if len(holding.Offsets) == 0 {
		return fmt.Errorf("%s 的 holding.offsets 不能为空", prefix)
	}
	seen := make(map[int]struct{}, len(holding.Offsets))
	for _, offset := range holding.Offsets {
		if offset < 0 || offset >= holding.Bars {
			return fmt.Errorf("%s 的 holding.offsets 必须在 0 到 bars-1 之间", prefix)
		}
		if _, exists := seen[offset]; exists {
			return fmt.Errorf("%s 的 holding.offsets 包含重复值 %d", prefix, offset)
		}
		seen[offset] = struct{}{}
	}
	return nil
}
