package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// TickerPlanner Ticker规划器
type TickerPlanner struct {
	base *BasePlanner
}

// NewTickerPlanner 创建Ticker规划器
func NewTickerPlanner(base *BasePlanner) *TickerPlanner {
	return &TickerPlanner{base: base}
}

// GetDataType 返回支持的数据类型
func (d *TickerPlanner) GetDataType() string {
	return model.DataTypeTicker
}

// GetTargetObjects 获取目标对象列表（交易对）
// 支持通配符匹配:
//   - "*" 表示全部对象
//   - "BTC-*" 匹配所有以 BTC- 开头的交易对
//   - "*-USDT" 匹配所有以 -USDT 结尾的交易对
func (d *TickerPlanner) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	log.DebugContextf(ctx, "[TickerPlanner] GetTargetObjects enter (ruleID=%s, dataSource=%s)",
		rule.RuleID, rule.DataSource)

	// 1. 从规则参数解析 objects（可能包含通配符）
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		log.ErrorContextf(ctx, "[TickerPlanner] Failed to parse collect params (ruleID=%s): %v",
			rule.RuleID, err)
		return nil, err
	}
	log.DebugContextf(ctx, "[TickerPlanner] Parsed objects patterns (ruleID=%s): %v",
		rule.RuleID, params.Objects)

	// 2. 从 SymbolProvider 获取所有可用标的
	allSymbols, err := d.base.GetSymbolProvider().GetSymbols(ctx, rule.DataSource)
	if err != nil {
		log.WarnContextf(ctx, "[TickerPlanner] Failed to get symbols from provider (ruleID=%s, dataSource=%s): %v",
			rule.RuleID, rule.DataSource, err)
		allSymbols = []string{}
	}
	log.InfoContextf(ctx, "[TickerPlanner] Got %d available symbols (ruleID=%s, dataSource=%s)",
		len(allSymbols), rule.RuleID, rule.DataSource)

	// 3. 如果规则没有指定 objects，返回所有可用标的
	if len(params.Objects) == 0 {
		log.InfoContextf(ctx, "[TickerPlanner] No patterns specified, using all %d symbols (ruleID=%s)",
			len(allSymbols), rule.RuleID)
		return allSymbols, nil
	}

	// 4. 解析通配符模式，返回匹配的对象
	matched := ResolveObjectPatterns(ctx, rule.RuleID, params.Objects, allSymbols)
	log.InfoContextf(ctx, "[TickerPlanner] Resolved %d objects from patterns %v (ruleID=%s)",
		len(matched), params.Objects, rule.RuleID)
	return matched, nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *TickerPlanner) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return "{}", err
	}

	// 默认产品类型为现货
	instType := params.InstType
	if instType == "" {
		instType = "SPOT"
	}

	taskParams := TaskParams{
		DataType:   rule.DataType,
		DataSource: rule.DataSource,
		InstType:   instType,
		Symbol:     object,
	}

	data, err := json.Marshal(taskParams)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

// GetMatchingNodes 获取匹配的节点列表
func (d *TickerPlanner) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
