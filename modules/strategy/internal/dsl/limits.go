package dsl

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// MaxLimitRounds 是超限转移的最多轮数：分配后又超限的部分再分一轮，三轮之后剩下的留现金。
const MaxLimitRounds = 3

// PortfolioLimits 是 portfolio.limits：单标的的多头、空头上限（占权益的比例，不随杠杆换算，同一标的各市场同方向的权重合计），
// 超出部分按 Overflow 转给一个来源（普通策略写规则 id，池写成员 id）本期同方向的其他标的；没写 Overflow 时留现金。
type PortfolioLimits struct {
	LongMax, ShortMax       quant.Decimal
	HasLongMax, HasShortMax bool
	// OverflowTo 是超出部分的去向：规则 id 或成员 id；空串表示留现金。
	OverflowTo string
}

func decodeLimits(node *yaml.Node, path string) (*PortfolioLimits, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	limits := &PortfolioLimits{}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "long_max":
			limits.LongMax, err = scalarDecimal(f.value, fieldPath)
			limits.HasLongMax = true
		case "short_max":
			limits.ShortMax, err = scalarDecimal(f.value, fieldPath)
			limits.HasShortMax = true
		case "overflow":
			limits.OverflowTo, err = decodeOverflow(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	return limits, nil
}

func decodeOverflow(node *yaml.Node, path string) (string, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return "", err
	}
	to := ""
	for _, f := range fields {
		if f.key != "to" {
			return "", fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if to, err = scalarRequiredText(f.value, path+".to"); err != nil {
			return "", err
		}
	}
	if to == "" {
		return "", fmt.Errorf("%s 需要写 to（规则 id 或成员 id）", path)
	}
	return to, nil
}

// validateLimits 校验上限的取值；去向是否存在由 validateLimitsTarget 按规则或成员检查。
func validateLimits(p *Portfolio) error {
	limits := p.Limits
	if limits == nil {
		return nil
	}
	if !limits.HasLongMax && !limits.HasShortMax {
		return errors.New("portfolio.limits 至少写 long_max、short_max 之一")
	}
	for name, value := range map[string]*quant.Decimal{"long_max": &limits.LongMax, "short_max": &limits.ShortMax} {
		if (name == "long_max" && !limits.HasLongMax) || (name == "short_max" && !limits.HasShortMax) {
			continue
		}
		if value.IsZero() || value.IsNegative() || value.Cmp(p.Leverage) > 0 {
			return fmt.Errorf("portfolio.limits.%s 必须大于 0 且不超过 portfolio.leverage", name)
		}
	}
	return nil
}

// validateLimitsTarget 检查 overflow.to 指向存在的来源：ids 是规则 id（普通策略）或成员 id（池），kind 用在提示里。
func validateLimitsTarget(p Portfolio, ids map[string]struct{}, kind string) error {
	if p.Limits == nil || p.Limits.OverflowTo == "" {
		return nil
	}
	if _, ok := ids[p.Limits.OverflowTo]; !ok {
		return fmt.Errorf("portfolio.limits.overflow.to %q 不是%s", p.Limits.OverflowTo, kind)
	}
	return nil
}
