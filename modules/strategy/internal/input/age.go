package input

import (
	"context"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// AgeRule 是一条规则的年龄要求：MinBars 是下限（没通过 N 根的判定即剔除），MaxBars 是上限（通过了 N 根的判定即剔除，
// 只保留新标的），AllMarkets 为真时要求在实例绑定的全部市场上都通过（age_markets: all），否则任一市场通过即算通过。
type AgeRule struct {
	MinBars    int  `json:"min_bars,omitempty"`
	MaxBars    int  `json:"max_bars,omitempty"`
	AllMarkets bool `json:"all_markets,omitempty"`
}

// ruleAges 返回各规则的年龄要求；所有规则都只按策略级 min_age_bars、按下单市场判断时返回 nil（现状，见 Resolved.RuleAges）。
func ruleAges(strategy dsl.Strategy) map[string]AgeRule {
	if !strategy.HasRuleAges() {
		return nil
	}
	ages := make(map[string]AgeRule, len(strategy.Rules))
	for _, rule := range strategy.Rules {
		universe := strategy.RuleUniverse(rule)
		ages[rule.ID] = AgeRule{MinBars: universe.MinAgeBars, MaxBars: universe.MaxAgeBars, AllMarkets: universe.AllMarkets()}
	}
	return ages
}

// AgeRuleOf 返回规则的年龄要求。
func (r Resolved) AgeRuleOf(ruleID string) AgeRule {
	if r.RuleAges != nil {
		return r.RuleAges[ruleID]
	}
	return AgeRule{MinBars: r.MinAgeBars}
}

// AgeLevels 返回需要做年龄判定的根数 N（去重升序）：策略级 min_age_bars 与各规则的上下限。
func (r Resolved) AgeLevels() []int {
	set := map[int]struct{}{}
	if r.MinAgeBars > 0 {
		set[r.MinAgeBars] = struct{}{}
	}
	for _, rule := range r.RuleAges {
		for _, n := range []int{rule.MinBars, rule.MaxBars} {
			if n > 0 {
				set[n] = struct{}{}
			}
		}
	}
	levels := make([]int, 0, len(set))
	for n := range set {
		levels = append(levels, n)
	}
	sort.Ints(levels)
	return levels
}

// LongestAge 返回最长的年龄判定根数；没有年龄要求时为 0。历史覆盖、回看与探针窗口按它检查。
func (r Resolved) LongestAge() int {
	levels := r.AgeLevels()
	if len(levels) == 0 {
		return 0
	}
	return levels[len(levels)-1]
}

// AgePassed 是一期的年龄判定结果：N → 市场 → 通过 N 根判定的标的。只含实际判定过的市场（不可用的市场不出现）。
type AgePassed map[int]map[string]map[string]struct{}

// MarketAgeProbe 判定一组标的在各市场是否通过 n 根的年龄判定，返回 市场 → 通过的标的。
type MarketAgeProbe func(ctx context.Context, n int, instruments []string) (map[string]map[string]struct{}, error)

// ApplyAge 用年龄探针计算各规则的 G(r)：对每个需要的 N 判定一次 ∪E(r)，再按规则的上下限与 age_markets 取剔除集合。
// markets 是实例绑定的全部市场（age_markets: all 要求在这些市场上都通过）。返回各 N 的判定结果，供按市场剔除行与重算使用。
func (s *Sets) ApplyAge(ctx context.Context, resolved Resolved, markets []string, probe MarketAgeProbe) (AgePassed, error) {
	levels := resolved.AgeLevels()
	if len(levels) == 0 {
		return AgePassed{}, nil
	}
	if probe == nil {
		return nil, fmt.Errorf("策略设置了年龄要求（min_age_bars 或 max_age_bars），但没有年龄探针")
	}
	instruments := s.Instruments()
	passed := make(AgePassed, len(levels))
	for _, n := range levels {
		got, err := probe(ctx, n, instruments)
		if err != nil {
			return nil, err
		}
		passed[n] = got
	}
	s.ApplyAgePassed(resolved, markets, passed)
	return passed, nil
}

// ApplyAgePassed 按已有的判定结果计算各规则的 G(r) 与其中因超过年龄上限剔除的部分。
func (s *Sets) ApplyAgePassed(resolved Resolved, markets []string, passed AgePassed) {
	if s.AgedOutMax == nil {
		s.AgedOutMax = map[string][]string{}
	}
	for ruleID, expected := range s.Expected {
		rule := resolved.AgeRuleOf(ruleID)
		if rule.MinBars <= 0 && rule.MaxBars <= 0 {
			continue
		}
		pass := func(n int, id string) bool {
			if rule.AllMarkets {
				for _, market := range markets {
					if _, ok := passed[n][market][id]; !ok {
						return false
					}
				}
				return len(markets) > 0
			}
			for _, ids := range passed[n] {
				if _, ok := ids[id]; ok {
					return true
				}
			}
			return false
		}
		var out, outMax []string
		for _, id := range expected {
			switch {
			case rule.MaxBars > 0 && pass(rule.MaxBars, id):
				out, outMax = append(out, id), append(outMax, id)
			case rule.MinBars > 0 && !pass(rule.MinBars, id):
				out = append(out, id)
			}
		}
		s.AgedOut[ruleID] = out
		if outMax != nil {
			s.AgedOutMax[ruleID] = outMax
		} else {
			delete(s.AgedOutMax, ruleID)
		}
	}
}
