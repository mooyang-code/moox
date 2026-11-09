package input

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
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

// CheckAgeMarkets 检查 age_markets: all 只用在绑定了两个市场的实例上（统一数据集，或现货、永续两个任务）：只绑一个市场时它与 each
// 没有区别，用户却以为在核对另一个市场，所以直接拒绝。单市场加参考市场（只用于年龄判定）以后再做。
func (r Resolved) CheckAgeMarkets() error {
	if len(r.Market.Markets()) >= 2 {
		return nil
	}
	ids := make([]string, 0)
	for id, rule := range r.RuleAges {
		if rule.AllMarkets {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return fmt.Errorf("规则 %s 写了 age_markets: all，但实例只绑定了 %s 一个市场：all 要在全部绑定市场上判断年龄，只有一个市场时与 each 相同；单市场加参考市场以后支持，现在请去掉 age_markets: all 或绑定现货、永续两个市场", strings.Join(ids, "、"), strings.Join(r.Market.Markets(), "、"))
}

// AgeLabel 说明 n 根的年龄判定来自哪些字段，供报错提示该改哪里：例如“年龄判定 24 根（universe.min_age_bars=24）”、
// “年龄判定 8760 根（规则 fresh 的 max_age_bars=8760）”。
func (r Resolved) AgeLabel(n int) string {
	var sources []string
	if r.MinAgeBars == n {
		sources = append(sources, fmt.Sprintf("universe.min_age_bars=%d", n))
	}
	ids := make([]string, 0, len(r.RuleAges))
	for id := range r.RuleAges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rule := r.RuleAges[id]
		if rule.MinBars == n && rule.MinBars != r.MinAgeBars {
			sources = append(sources, fmt.Sprintf("规则 %s 的 min_age_bars=%d", id, n))
		}
		if rule.MaxBars == n {
			sources = append(sources, fmt.Sprintf("规则 %s 的 max_age_bars=%d", id, n))
		}
	}
	if len(sources) == 0 {
		return fmt.Sprintf("年龄判定 %d 根", n)
	}
	return fmt.Sprintf("年龄判定 %d 根（%s）", n, strings.Join(sources, "、"))
}

// LongestAgeLabel 说明最长的年龄判定：根数是各规则 min_age_bars、max_age_bars 的最大值，括号里是它的来源字段。
func (r Resolved) LongestAgeLabel() string {
	longest := r.LongestAge()
	label := r.AgeLabel(longest)
	if len(r.RuleAges) == 0 {
		return label
	}
	return strings.Replace(label, "年龄判定", "年龄判定（min_age_bars、max_age_bars 的最大值）", 1)
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

// AgeListed 是实例绑定的每个市场本期列出的标的（市场 → 标的）：键是全部绑定市场，本期不可用的市场也在内。
type AgeListed map[string]map[string]struct{}

// ApplyAge 用年龄探针计算各规则的 G(r)：对每个需要的 N 判定一次 ∪E(r)，再按规则的上下限与 age_markets 取剔除集合（见 ApplyAgePassed）。
// 返回各 N 的判定结果，供按市场剔除行与重算使用。
func (s *Sets) ApplyAge(ctx context.Context, resolved Resolved, listed AgeListed, probe MarketAgeProbe) (AgePassed, error) {
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
	s.ApplyAgePassed(resolved, listed, passed)
	return passed, nil
}

// ApplyAgePassed 按已有的判定结果计算各规则的 G(r) 与其中因超过年龄上限剔除的部分。一个市场“判定过”指它在 passed[N] 里出现
// （本期可用、做了探针）；本期不可用的市场没有判定结果。
//   - age_markets: each（默认）：min_age_bars 在任一判定过的市场通过即算通过（现状）。max_age_bars 在任一判定过的市场通过 N 即不是新标的；
//     标的列在某个本期不可用的市场里时，无法确认它在那里是不是老标的，按保守口径算作不是新标的、剔除。
//   - age_markets: all：标的必须在每个绑定市场都列出、且这些市场本期都判定过，否则 min_age_bars 与 max_age_bars 都不通过、剔除；
//     满足时年龄按最晚出现的市场算：min 要求每个市场都通过，max 在每个市场都通过 N 时才不是新标的。
func (s *Sets) ApplyAgePassed(resolved Resolved, listed AgeListed, passed AgePassed) {
	if s.AgedOutReasons == nil {
		s.AgedOutReasons = map[string]map[string]string{}
	}
	judged := func(n int, market string) bool {
		_, ok := passed[n][market]
		return ok
	}
	for ruleID, expected := range s.Expected {
		rule := resolved.AgeRuleOf(ruleID)
		if rule.MinBars <= 0 && rule.MaxBars <= 0 {
			continue
		}
		passAny := func(n int, id string) bool {
			for _, ids := range passed[n] {
				if _, ok := ids[id]; ok {
					return true
				}
			}
			return false
		}
		passAll := func(n int, id string) bool {
			for market := range listed {
				if _, ok := passed[n][market][id]; !ok {
					return false
				}
			}
			return len(listed) > 0
		}
		// everywhere：标的在每个绑定市场都列出，且这些市场在需要的每个 N 上都判定过。
		everywhere := func(id string) bool {
			for market, ids := range listed {
				if _, ok := ids[id]; !ok {
					return false
				}
				for _, n := range []int{rule.MinBars, rule.MaxBars} {
					if n > 0 && !judged(n, market) {
						return false
					}
				}
			}
			return len(listed) > 0
		}
		// unknown：标的列在某个没有判定 n 的市场里（本期不可用），不知道它在那里的年龄。
		unknown := func(n int, id string) bool {
			for market, ids := range listed {
				if _, ok := ids[id]; ok && !judged(n, market) {
					return true
				}
			}
			return false
		}
		var out []string
		reasons := map[string]string{}
		for _, id := range expected {
			if rule.AllMarkets {
				switch {
				case !everywhere(id):
					out = append(out, id)
					reasons[id] = engine.ReasonAgeMarkets
				case rule.MaxBars > 0 && passAll(rule.MaxBars, id):
					out = append(out, id)
					reasons[id] = engine.ReasonAgedOutMax
				case rule.MinBars > 0 && !passAll(rule.MinBars, id):
					out = append(out, id)
				}
				continue
			}
			switch {
			case rule.MaxBars > 0 && (passAny(rule.MaxBars, id) || unknown(rule.MaxBars, id)):
				out = append(out, id)
				reasons[id] = engine.ReasonAgedOutMax
			case rule.MinBars > 0 && !passAny(rule.MinBars, id):
				out = append(out, id)
			}
		}
		s.AgedOut[ruleID] = out
		if len(reasons) > 0 {
			s.AgedOutReasons[ruleID] = reasons
		} else {
			delete(s.AgedOutReasons, ruleID)
		}
	}
}
