package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// taskRequirement 由 DSL 推出建实例要绑定的因子任务（管理台据此列出要选的任务）：
//   - 普通策略：写了 market 时主帧市场就是模式涉及的市场；没写时只绑一个主帧任务，市场由所选任务决定（股票策略不写 market）。
//   - 参考市场（market.reference）各绑一个任务，键是市场；附加来源按主帧的每个市场各绑一个，键是 来源名:市场。
//   - 策略池：主帧市场取各成员所需市场的并集；没写 market 的成员按股票成员处理，不能与现货、永续成员混放。
//
// 读不到成员定义等问题记进 problems，不报错：页面照常展示定义，只是不能据此建实例。
func (s *Service) taskRequirement(ctx context.Context, strategy dsl.Strategy) *strategypb.StrategyTaskRequirement {
	requirement := &strategypb.StrategyTaskRequirement{Pool: strategy.IsPool(), TaskKeys: []string{}, Sources: []*strategypb.StrategySourceRequirement{}, Problems: []string{}, ReferenceMarkets: []string{}}
	for _, source := range strategy.Sources {
		requirement.Sources = append(requirement.Sources, &strategypb.StrategySourceRequirement{Name: source.Name, Bar: source.Bar})
	}
	var markets []string
	if strategy.IsPool() {
		markets, requirement.Problems = s.poolMarkets(ctx, strategy)
	} else if strategy.MarketDeclared {
		markets = strategy.Market.Markets()
	}
	if len(markets) == 0 || len(requirement.Problems) > 0 {
		return requirement
	}
	requirement.MarketDeclared = true
	requirement.TaskKeys = append(requirement.TaskKeys, markets...)
	requirement.TaskKeys = append(requirement.TaskKeys, strategy.ReferenceMarkets...)
	requirement.ReferenceMarkets = append(requirement.ReferenceMarkets, strategy.ReferenceMarkets...)
	for _, source := range strategy.Sources {
		for _, market := range markets {
			requirement.TaskKeys = append(requirement.TaskKeys, dsl.TaskKey(source.Name, market))
		}
	}
	return requirement
}

// poolMarkets 返回策略池要绑定的主帧市场（现货、永续、股票的先后）与读成员时遇到的问题。
func (s *Service) poolMarkets(ctx context.Context, pool dsl.Strategy) ([]string, []string) {
	if len(pool.Pool.Members) == 0 {
		return nil, []string{"策略池没有写成员（strategy_pool.members）"}
	}
	var problems, stockMembers []string
	needed := map[string]bool{}
	for _, member := range pool.Pool.Members {
		label := "成员 " + member.ID
		def, err := s.Store.GetDefinition(ctx, member.StrategyID)
		if err != nil || def.DeletedAt != nil {
			problems = append(problems, fmt.Sprintf("%s 引用的策略 %s 不存在（定义已删除，或读取失败）", label, member.StrategyID))
			continue
		}
		strategy, err := dsl.Parse([]byte(def.DSLYaml))
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s 引用的策略 %s 的 DSL 无效：%s", label, member.StrategyID, publicMessage(err)))
		case strategy.IsPool():
			problems = append(problems, fmt.Sprintf("%s 引用的策略 %s 本身是策略池，策略池不能嵌套", label, member.StrategyID))
		case !strategy.MarketDeclared:
			stockMembers = append(stockMembers, label)
		default:
			for _, market := range strategy.Market.Markets() {
				needed[market] = true
			}
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	if len(stockMembers) > 0 && len(needed) > 0 {
		return nil, []string{fmt.Sprintf("%s 引用的策略没有写 market，按股票成员处理；股票成员不能与现货、永续成员放在同一个池里（现货、永续成员要显式写 market）", strings.Join(stockMembers, "、"))}
	}
	if len(stockMembers) > 0 {
		needed[dsl.MarketEquity] = true
	}
	var markets []string
	for _, market := range []string{dsl.MarketSpot, dsl.MarketSwap, dsl.MarketEquity} {
		if needed[market] {
			markets = append(markets, market)
		}
	}
	return markets, nil
}
