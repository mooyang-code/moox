package dsl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// universe.age_markets 的取值：each（默认）按标的在下单市场上的数据判断年龄；all 要求在实例绑定的全部市场上都通过。
const (
	AgeMarketsEach = "each"
	AgeMarketsAll  = "all"
)

// universe 的字段名：规则级 universe 逐项覆盖策略级，写了显式 pool 的规则只能写年龄类字段。
const (
	universeTags        = "tags"
	universeExcludeTags = "exclude_tags"
	universeInclude     = "include"
	universeExclude     = "exclude"
	universeMinAge      = "min_age_bars"
	universeMaxAge      = "max_age_bars"
	universeAgeMarkets  = "age_markets"
)

// RuleUniverse 是规则级 universe：Fields 是规则里写了的字段，只有这些字段覆盖策略级。
type RuleUniverse struct {
	Universe
	Fields map[string]struct{}
}

// has 报告规则级 universe 是否写了字段 name。
func (u *RuleUniverse) has(name string) bool {
	if u == nil {
		return false
	}
	_, ok := u.Fields[name]
	return ok
}

// OverridesMembers 报告规则级 universe 是否改写了标的范围（标签与黑白名单）；只写年龄类字段时规则沿用策略级的默认集合。
func (u *RuleUniverse) OverridesMembers() bool {
	return u.has(universeTags) || u.has(universeExcludeTags) || u.has(universeInclude) || u.has(universeExclude)
}

// RuleUniverse 返回规则实际使用的 universe：规则级写了的字段逐项覆盖策略级，没写的沿用策略级。
func (s Strategy) RuleUniverse(rule Rule) Universe {
	merged := s.Universe
	u := rule.Universe
	if u == nil {
		return merged
	}
	if u.has(universeTags) {
		merged.Tags = u.Tags
	}
	if u.has(universeExcludeTags) {
		merged.ExcludeTags = u.ExcludeTags
	}
	if u.has(universeInclude) {
		merged.Include = u.Include
	}
	if u.has(universeExclude) {
		merged.Exclude = u.Exclude
	}
	if u.has(universeMinAge) {
		merged.MinAgeBars = u.MinAgeBars
	}
	if u.has(universeMaxAge) {
		merged.MaxAgeBars = u.MaxAgeBars
	}
	if u.has(universeAgeMarkets) {
		merged.AgeMarkets = u.AgeMarkets
	}
	return merged
}

// AllMarkets 报告 universe 是否要求在全部绑定市场上判断年龄（age_markets: all）。
func (u Universe) AllMarkets() bool { return u.AgeMarkets == AgeMarketsAll }

// HasRuleAges 报告是否有规则的年龄要求不同于“策略级 min_age_bars、按下单市场判断”：写了规则级年龄字段、max_age_bars 或 age_markets: all。
// 为假时各规则都只按策略级 min_age_bars 判断（现状）。
func (s Strategy) HasRuleAges() bool {
	for _, rule := range s.Rules {
		universe := s.RuleUniverse(rule)
		if universe.MinAgeBars != s.Universe.MinAgeBars || universe.MaxAgeBars > 0 || universe.AllMarkets() {
			return true
		}
	}
	return false
}

// LongestAgeBars 返回策略里最长的年龄判定根数（策略级与各规则的 min_age_bars、max_age_bars 取大），用于历史覆盖与回看的检查。
func (s Strategy) LongestAgeBars() int {
	longest := s.Universe.MinAgeBars
	for _, rule := range s.Rules {
		universe := s.RuleUniverse(rule)
		longest = max(longest, universe.MinAgeBars, universe.MaxAgeBars)
	}
	return longest
}

func decodeUniverse(node *yaml.Node, path string) (Universe, map[string]struct{}, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return Universe{}, nil, err
	}
	var universe Universe
	written := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case universeTags:
			universe.Tags, err = stringList(f.value, fieldPath)
		case universeExcludeTags:
			universe.ExcludeTags, err = stringList(f.value, fieldPath)
		case universeInclude:
			universe.Include, err = stringList(f.value, fieldPath)
		case universeExclude:
			universe.Exclude, err = stringList(f.value, fieldPath)
		case universeMinAge:
			universe.MinAgeBars, err = scalarInt(f.value, fieldPath)
		case universeMaxAge:
			universe.MaxAgeBars, err = scalarInt(f.value, fieldPath)
		case universeAgeMarkets:
			universe.AgeMarkets, err = scalarRequiredText(f.value, fieldPath)
		default:
			return Universe{}, nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Universe{}, nil, err
		}
		written[f.key] = struct{}{}
	}
	return universe, written, nil
}

func decodeRuleUniverse(node *yaml.Node, path string) (*RuleUniverse, error) {
	universe, written, err := decodeUniverse(node, path)
	if err != nil {
		return nil, err
	}
	return &RuleUniverse{Universe: universe, Fields: written}, nil
}

// validateUniverseAges 校验一份 universe 的年龄字段取值。where 是报错用的位置（universe 或“规则 x 的 universe”）。
func validateUniverseAges(where string, universe Universe) error {
	switch {
	case universe.MinAgeBars < 0:
		return fmt.Errorf("%s.min_age_bars 不能为负数", where)
	case universe.MinAgeBars > MaxCount:
		return fmt.Errorf("%s.min_age_bars 不能超过 %d", where, MaxCount)
	case universe.MaxAgeBars < 0:
		return fmt.Errorf("%s.max_age_bars 不能为负数", where)
	case universe.MaxAgeBars > MaxCount:
		return fmt.Errorf("%s.max_age_bars 不能超过 %d", where, MaxCount)
	case universe.AgeMarkets != "" && universe.AgeMarkets != AgeMarketsEach && universe.AgeMarkets != AgeMarketsAll:
		return fmt.Errorf("%s.age_markets 只能是 each 或 all", where)
	case universe.MaxAgeBars > 0 && universe.MinAgeBars >= universe.MaxAgeBars:
		return fmt.Errorf("%s.min_age_bars 必须小于 max_age_bars，否则没有标的能入选", where)
	}
	return nil
}

// validateRuleUniverse 校验规则级 universe 与 allow_empty：显式 pool 的规则只能写年龄类字段；
// 规则实际的 max_age_bars 大于 0 时必须写 allow_empty: true。
func validateRuleUniverse(prefix string, s *Strategy, rule *Rule) error {
	if u := rule.Universe; u != nil {
		if rule.Pool.Explicit && u.OverridesMembers() {
			return fmt.Errorf("%s 写了显式 pool，规则级 universe 只能写 min_age_bars、max_age_bars、age_markets（标签与黑白名单不作用于显式 pool）", prefix)
		}
		if err := validateUniverseAges(prefix+" 的 universe", u.Universe); err != nil {
			return err
		}
	}
	merged := s.RuleUniverse(*rule)
	if err := validateUniverseAges(prefix+" 实际使用的 universe", merged); err != nil {
		return err
	}
	if merged.MaxAgeBars > 0 && !rule.AllowEmpty {
		return fmt.Errorf("%s 使用了 max_age_bars，必须同时写 allow_empty: true：只保留新标的的规则经常没有候选，不写会让整期守门跳过、拖垮整个策略", prefix)
	}
	return nil
}
