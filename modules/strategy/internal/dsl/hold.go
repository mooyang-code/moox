package dsl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Hold 是 rank 规则的进出场持有：持有中满足 Exit、不再满足 filter、持满 MaxBars 根（0 表示不限）就出场；
// 空位按 score 名次从满足 Entry（不写表示不限制）的候选里补足，目标持有数由 select 换算。出场后 CooldownBars 根内不能再入场。
type Hold struct {
	Entry        string
	Exit         string
	MaxBars      int
	CooldownBars int
	// AllowReentry 为真时，候选只看 Entry：出场条件为真的标的也可以入场，本根刚出场的标的也可以当根补回（持有根数重新计）。
	// 只能与 cooldown_bars 为 0 同用（冷却与当根补回互相矛盾）。
	AllowReentry bool
}

func decodeHold(node *yaml.Node, path string) (*Hold, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	hold := &Hold{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "entry":
			hold.Entry, err = scalarRequiredText(f.value, fieldPath)
		case "exit":
			hold.Exit, err = scalarRequiredText(f.value, fieldPath)
		case "max_bars":
			hold.MaxBars, err = scalarInt(f.value, fieldPath)
		case "cooldown_bars":
			hold.CooldownBars, err = scalarInt(f.value, fieldPath)
		case "allow_reentry":
			hold.AllowReentry, err = scalarBool(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return hold, nil
}

// validateHold 校验 hold 的取值与互斥：hold 自己维护持有集合，不能再叠加 buffer、holding 的 bars/offsets、then、range 与 filter_after；
// 可以与 holding.calendar 同写（按日历判进出场），这时只能有一个批次。
func validateHold(prefix string, rule *Rule) error {
	hold := rule.Hold
	if hold.MaxBars < 0 || hold.MaxBars > MaxCount {
		return fmt.Errorf("%s 的 hold.max_bars 必须在 0 到 %d 之间", prefix, MaxCount)
	}
	if hold.CooldownBars < 0 || hold.CooldownBars > MaxCount {
		return fmt.Errorf("%s 的 hold.cooldown_bars 必须在 0 到 %d 之间", prefix, MaxCount)
	}
	if hold.AllowReentry && hold.CooldownBars > 0 {
		return fmt.Errorf("%s 的 hold.allow_reentry 不能与大于 0 的 cooldown_bars 同时使用（允许当根补回就没有冷却）", prefix)
	}
	switch {
	case rule.Select.Buffer > 0:
		return fmt.Errorf("%s 的 hold 不能与 select.buffer 同时使用", prefix)
	case rule.Select.Then != nil:
		return fmt.Errorf("%s 的 hold 不能与 select.then 同时使用", prefix)
	case rule.Select.HasRange():
		return fmt.Errorf("%s 的 hold 不能与 select.range 同时使用", prefix)
	case rule.FilterAfter != "":
		return fmt.Errorf("%s 的 hold 不能与 filter_after 同时使用", prefix)
	case rule.Holding != nil && !rule.Holding.IsCalendar():
		return fmt.Errorf("%s 的 hold 不能与 holding 的 bars/offsets 同时使用（可以与 holding.calendar 同写）", prefix)
	case rule.Holding.IsCalendar() && len(rule.Holding.Days) != 1:
		return fmt.Errorf("%s 的 hold 与 holding.calendar 同写时，holding.days 只能有一个值", prefix)
	}
	return nil
}
