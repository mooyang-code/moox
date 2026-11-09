package dsl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// 仓位系数与缺数冻结的取值范围。
const (
	// MaxScale 是 scale.max 的上限。
	MaxScale = 10.0
	// MaxKeepBars 是 on_missing.keep_bars 的上限。
	MaxKeepBars = 1000
)

// Scale 是规则输出权重上逐标的乘的系数：Expr 是数值表达式（逐行求值），结果截到 [0, Max]，乘掉的部分留现金。
type Scale struct {
	Expr string
	Max  float64
}

// OnMissing 是持有标的缺数时的处理：连续缺数不超过 KeepBars 根时按冻结保持上期目标，超过后按现状移出。
type OnMissing struct {
	KeepBars int
}

func decodeScale(node *yaml.Node, path string) (*Scale, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	scale := &Scale{Max: 1}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "expr":
			scale.Expr, err = scalarRequiredText(f.value, fieldPath)
		case "max":
			scale.Max, err = scalarFloat(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if scale.Expr == "" {
		return nil, fmt.Errorf("%s 缺少 expr", path)
	}
	if scale.Max <= 0 || scale.Max > MaxScale {
		return nil, fmt.Errorf("%s.max 必须在 (0, %g] 之间", path, MaxScale)
	}
	return scale, nil
}

func decodeOnMissing(node *yaml.Node, path string) (*OnMissing, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	onMissing := &OnMissing{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "keep_bars":
			onMissing.KeepBars, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if onMissing.KeepBars < 1 || onMissing.KeepBars > MaxKeepBars {
		return nil, fmt.Errorf("%s.keep_bars 必须在 1 到 %d 之间", path, MaxKeepBars)
	}
	return onMissing, nil
}

// HasOnMissing 报告策略里是否有规则写了 on_missing：只有这时冻结才按“只由持有该标的的规则决定”合并（见 engine 的 frozenSet）。
func (s Strategy) HasOnMissing() bool {
	for _, rule := range s.Rules {
		if rule.OnMissing != nil {
			return true
		}
	}
	return false
}
