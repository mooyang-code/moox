package dsl

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"gopkg.in/yaml.v3"
)

// 市场取值。select 可以是 spot、swap、both，prefer 可以是 spot、swap；equity 只由绑定解析写入（股票不允许在 DSL 里写 market）。
const (
	MarketSpot   = "spot"
	MarketSwap   = "swap"
	MarketBoth   = "both"
	MarketEquity = "equity"
)

// Market 是策略的市场模式：Select 决定从哪个市场的币里选，Prefer 决定一个币在两个市场都可用时优先在哪个市场下单。
// 参考市场（market.reference）不属于模式，见 Strategy.ReferenceMarkets。
type Market struct {
	Select string `json:"select"`
	Prefer string `json:"prefer"`
}

// SingleMarket 返回只在一个市场（spot、swap 或 equity）里选币、下单的模式。
func SingleMarket(market string) Market { return Market{Select: market, Prefer: market} }

// IsZero 报告模式还没有确定（DSL 没写 market，也还没有按绑定的任务填入默认值）。
func (m Market) IsZero() bool { return m.Select == "" && m.Prefer == "" }

// Markets 返回模式涉及的市场（输出可能落在这些市场，也需要这些市场的 View），现货在前、永续在后。
func (m Market) Markets() []string {
	switch {
	case m.Select == MarketEquity:
		return []string{MarketEquity}
	case m.Select == MarketBoth || m.Select != m.Prefer:
		return []string{MarketSpot, MarketSwap}
	default:
		return []string{m.Select}
	}
}

// Has 报告模式是否涉及某个市场。
func (m Market) Has(market string) bool {
	for _, item := range m.Markets() {
		if item == market {
			return true
		}
	}
	return false
}

// Single 报告模式只涉及一个市场。
func (m Market) Single() bool { return len(m.Markets()) == 1 }

// SpotLike 报告市场按现货规则约束：现货与股票都不能做空、不能加杠杆。
func SpotLike(market string) bool { return market == MarketSpot || market == MarketEquity }

// CanOutputSpotLike 报告模式可能输出现货（或股票）目标。
func (m Market) CanOutputSpotLike() bool { return m.Has(MarketSpot) || m.Has(MarketEquity) }

// String 返回 select/prefer 的简写，用在错误提示里。
func (m Market) String() string {
	if m.Select == m.Prefer {
		return m.Select
	}
	return "select=" + m.Select + ", prefer=" + m.Prefer
}

// decodeMarket 解析 market 段：select、prefer 是模式，reference 是参考市场。
func decodeMarket(node *yaml.Node) (Market, []string, error) {
	fields, err := mappingFields(node, "market")
	if err != nil {
		return Market{}, nil, err
	}
	var market Market
	var reference []string
	for _, f := range fields {
		path := "market." + f.key
		switch f.key {
		case "select":
			market.Select, err = scalarRequiredText(f.value, path)
		case "prefer":
			market.Prefer, err = scalarRequiredText(f.value, path)
		case "reference":
			reference, err = stringList(f.value, path)
		default:
			return Market{}, nil, fmt.Errorf("market 包含未知字段 %q", f.key)
		}
		if err != nil {
			return Market{}, nil, err
		}
	}
	return market, reference, nil
}

// validateReferenceMarkets 校验 market.reference：只能是 spot 或 swap、不能是模式已涉及的市场（只有单市场策略能写），并且要有规则写了
// age_markets: all，否则参考市场的数据不会被用到。
func validateReferenceMarkets(s *Strategy) error {
	if len(s.ReferenceMarkets) == 0 {
		return nil
	}
	for _, market := range s.ReferenceMarkets {
		switch {
		case market != MarketSpot && market != MarketSwap:
			return fmt.Errorf("market.reference 的 %q 不受支持，只能是 spot 或 swap", market)
		case s.Market.Has(market):
			return fmt.Errorf("market.reference 的 %s 已在 select/prefer 里：参考市场只能是策略不选择、不下单的那个市场（只有单市场策略能写参考市场）", market)
		}
	}
	for _, rule := range s.Rules {
		if s.RuleUniverse(rule).AllMarkets() {
			return nil
		}
	}
	return errors.New("写了 market.reference，但没有规则使用 age_markets: all：参考市场只用于 age_markets: all 的年龄判定，请去掉 market.reference 或在 universe 里写 age_markets: all")
}

// validateMarketCombination 校验 market 的写法只能是 5 种组合之一：spot/spot、swap/swap、both/spot、both/swap、spot/swap。
// select 为 spot 或 swap 时可以省略 prefer（取同一个市场）；select 为 both 时必须写 prefer。
func validateMarketCombination(market *Market) error {
	switch market.Select {
	case MarketSpot, MarketSwap:
		if market.Prefer == "" {
			market.Prefer = market.Select
		}
	case MarketBoth:
		if market.Prefer == "" {
			return errors.New("market.select 为 both 时必须写 market.prefer（spot 或 swap）")
		}
	case "":
		return errors.New("market 必须写 select（spot、swap 或 both）")
	case MarketEquity:
		return errors.New("股票（equity）不需要也不允许写 market")
	default:
		return fmt.Errorf("market.select %q 不受支持，可选：spot、swap、both", market.Select)
	}
	if market.Prefer != MarketSpot && market.Prefer != MarketSwap {
		return fmt.Errorf("market.prefer %q 不受支持，可选：spot、swap", market.Prefer)
	}
	if market.Select == MarketSwap && market.Prefer == MarketSpot {
		return errors.New("market 的组合不受支持：只从永续里选币时只能在永续下单（select: swap 必须配 prefer: swap）；" +
			"支持的组合：spot/spot、swap/swap、both/spot、both/swap、spot/swap")
	}
	return nil
}

// ValidateMode 校验策略与有效市场模式（DSL 写出的，或没写时按绑定任务的市场取的默认值）能否同时成立：
// 纯现货与股票不能做空、不能加杠杆；tranche 只能用在单个市场的策略（S1 暂不支持双市场 tranche）；可能输出现货的模式（纯现货与股票）里 tranche 不能做空、只能按权重持有
// （tranche 内部把同一个币的多空合成一个净值，没法再按方向分市场；按数量持有会让权重之和略大于 1）。
// 绑定了任务之后、启用与试算之前调用。
func ValidateMode(s Strategy, mode Market) error {
	if mode.IsZero() {
		return errors.New("策略的市场模式未确定")
	}
	if !mode.Single() && len(s.FactorParams) > 0 {
		return fmt.Errorf("%s 策略要绑定现货、永续两个因子任务，不支持 factor_params（两个任务的可变成员键可能重名，参数回写会冲突）", modeLabel(mode))
	}
	if !mode.Single() && s.HasTranche() {
		return fmt.Errorf("%s 策略不支持 tranche 规则：S1 暂不支持双市场 tranche，原因是一个市场本期不可用时 tranche 的持仓无法冻结（tranche 内部把同一个币的多空合成一个净值，也不参与冻结），会把这个市场里的持仓卖掉；tranche 只能用在单个市场的策略（spot/spot 或 swap/swap）", modeLabel(mode))
	}
	for _, rule := range s.Rules {
		if rule.Market != "" && mode.Single() && rule.Market != mode.Select {
			return fmt.Errorf("规则 %s 写了 market: %s，与%s策略不一致：只有策略涉及现货、永续两个市场时，规则才能另选市场，单市场策略的规则 market 必须与策略相同", rule.ID, rule.Market, modeLabel(mode))
		}
	}
	if !mode.Has(MarketSwap) {
		if s.Portfolio.Leverage.Cmp(quant.One()) > 0 {
			return fmt.Errorf("%s 策略不允许 portfolio.leverage 大于 1（当前 %s），加杠杆需要永续市场", modeLabel(mode), s.Portfolio.Leverage.String())
		}
		for _, rule := range s.Rules {
			if rule.Type != RuleTypeTranche && rule.Side == SideShort {
				return fmt.Errorf("%s 策略不允许规则 %s 做空，空头只能在永续市场", modeLabel(mode), rule.ID)
			}
		}
	}
	if !mode.CanOutputSpotLike() {
		return nil
	}
	for _, rule := range s.Rules {
		if rule.Type != RuleTypeTranche || rule.Tranche == nil {
			continue
		}
		for _, sub := range rule.Tranche.Subs {
			if sub.ShortWeight > 0 {
				return fmt.Errorf("规则 %s 的 tranche 子策略做空：%s 策略的 tranche 不能做空（tranche 内部把同一个币的多空合成一个净值，没法再按方向分市场）", rule.ID, modeLabel(mode))
			}
			if sub.SizingMethod != SizingWeight {
				return fmt.Errorf("规则 %s 的 tranche 子策略按数量持有：%s 策略的 tranche 只能用 sizing.method: weight（按数量持有会让权重之和略大于 1）", rule.ID, modeLabel(mode))
			}
		}
	}
	return nil
}

// validateRuleMarket 校验规则级市场的写法（与模式无关的部分）：只能是 spot 或 swap；写了它的策略必须显式写 market（模式在保存时就要确定，
// 单市场策略的一致性检查才能在保存时做）；现货规则不能做空。
func validateRuleMarket(s *Strategy, rule *Rule) error {
	if rule.Market == "" {
		return nil
	}
	prefix := "规则 " + rule.ID
	switch {
	case rule.Market != MarketSpot && rule.Market != MarketSwap:
		return fmt.Errorf("%s 的 market %q 不受支持，规则级 market 只能是 spot 或 swap", prefix, rule.Market)
	case !s.MarketDeclared:
		return fmt.Errorf("%s 写了 market，策略也必须显式写 market（规则级市场只在策略涉及现货、永续两个市场时可以另选；股票策略不支持）", prefix)
	case rule.Market == MarketSpot && rule.Side == SideShort:
		return fmt.Errorf("%s 是现货规则（market: spot），不能做空：空头只能在永续市场", prefix)
	}
	return nil
}

// HasRuleMarket 报告是否有规则写了规则级 market。
func (s Strategy) HasRuleMarket() bool {
	for _, rule := range s.Rules {
		if rule.Market != "" {
			return true
		}
	}
	return false
}

// RuleMarket 返回规则在模式 mode 下实际使用的规则级市场：只有模式涉及两个市场时才生效，单市场模式下（规则 market 只能与策略相同）返回空串，
// 与没写相同。
func RuleMarket(mode Market, rule Rule) string {
	if mode.Single() {
		return ""
	}
	return rule.Market
}

func modeLabel(mode Market) string {
	switch {
	case mode.Select == MarketEquity:
		return "股票"
	case mode.Single() && mode.Select == MarketSpot:
		return "现货"
	case mode.Single():
		return "永续"
	default:
		return "混合市场（" + mode.String() + "）"
	}
}
