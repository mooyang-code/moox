package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// buildObject 编码原子对象：symbol + interval -> "symbol@interval"
func buildObject(symbol, interval string) string {
	return symbol + "@" + interval
}

// parseObject 解码原子对象："symbol@interval" -> (symbol, interval)
// 从右向左查找@，处理symbol可能包含@的边缘情况
func parseObject(object string) (symbol, interval string) {
	for i := len(object) - 1; i >= 0; i-- {
		if object[i] == '@' {
			return object[:i], object[i+1:]
		}
	}
	// 没有找到@，整个字符串作为symbol
	return object, ""
}

// KlinePlanner K线规划器
type KlinePlanner struct {
	base *BasePlanner
}

// NewKlinePlanner 创建K线规划器
func NewKlinePlanner(base *BasePlanner) *KlinePlanner {
	return &KlinePlanner{base: base}
}

// GetDataType 返回支持的数据类型
func (d *KlinePlanner) GetDataType() string {
	return model.DataTypeKline
}

// GetTargetObjects 获取目标对象列表（原子任务展开：symbol × interval）
// K线任务按 (symbol, interval) 正交展开，每个组合生成一个原子任务
// 例如：symbols=[BTC-USDT, ETH-USDT], intervals=[1m, 5m]
//      展开为：BTC-USDT@1m, BTC-USDT@5m, ETH-USDT@1m, ETH-USDT@5m
func (d *KlinePlanner) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	log.DebugContextf(ctx, "[KlinePlanner] GetTargetObjects enter (ruleID=%s, dataSource=%s)",
		rule.RuleID, rule.DataSource)

	// 1. 从规则参数解析 objects 和 intervals
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		log.ErrorContextf(ctx, "[KlinePlanner] Failed to parse collect params (ruleID=%s): %v",
			rule.RuleID, err)
		return nil, err
	}
	log.DebugContextf(ctx, "[KlinePlanner] Parsed params (ruleID=%s): objects=%v, intervals=%v",
		rule.RuleID, params.Objects, params.Intervals)

	// 2. 从 SymbolProvider 获取所有可用标的
	allSymbols, err := d.base.GetSymbolProvider().GetSymbols(ctx, rule.DataSource)
	if err != nil {
		log.WarnContextf(ctx, "[KlinePlanner] Failed to get symbols from provider (ruleID=%s, dataSource=%s): %v",
			rule.RuleID, rule.DataSource, err)
		allSymbols = []string{}
	}
	log.InfoContextf(ctx, "[KlinePlanner] Got %d available symbols (ruleID=%s, dataSource=%s)",
		len(allSymbols), rule.RuleID, rule.DataSource)

	// 3. 解析通配符模式，得到匹配的symbols
	var matchedSymbols []string
	if len(params.Objects) == 0 {
		log.InfoContextf(ctx, "[KlinePlanner] No patterns specified, using all %d symbols (ruleID=%s)",
			len(allSymbols), rule.RuleID)
		matchedSymbols = allSymbols
	} else {
		matchedSymbols = ResolveObjectPatterns(ctx, rule.RuleID, params.Objects, allSymbols)
		log.InfoContextf(ctx, "[KlinePlanner] Resolved %d symbols from patterns %v (ruleID=%s)",
			len(matchedSymbols), params.Objects, rule.RuleID)
	}

	// 4. 如果没有intervals配置，使用默认值["1m"]
	intervals := params.Intervals
	if len(intervals) == 0 {
		intervals = []string{"1m"}
		log.WarnContextf(ctx, "[KlinePlanner] No intervals specified, using default [1m] (ruleID=%s)",
			rule.RuleID)
	}

	// 5. 正交展开：symbol × interval
	// 使用 "symbol@interval" 格式作为对象标识
	var atomicObjects []string
	for _, symbol := range matchedSymbols {
		for _, interval := range intervals {
			atomicObjects = append(atomicObjects, buildObject(symbol, interval))
		}
	}

	log.InfoContextf(ctx, "[KlinePlanner] Expanded to %d atomic tasks (symbols=%d × intervals=%d, ruleID=%s)",
		len(atomicObjects), len(matchedSymbols), len(intervals), rule.RuleID)
	return atomicObjects, nil
}

// BuildTaskParams 为指定对象构建任务参数（原子任务）
// object格式为 "symbol@interval"，例如 "BTC-USDT@1m"
// 生成的TaskParams.Intervals只包含单个interval
func (d *KlinePlanner) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return "{}", err
	}

	// 解析原子对象：symbol@interval
	symbol, interval := parseObject(object)
	
	// 如果没有找到@，使用规则配置的第一个interval或默认值
	if interval == "" {
		if len(params.Intervals) > 0 {
			interval = params.Intervals[0]
		} else {
			interval = "1m"
		}
	}

	// 默认产品类型为现货
	instType := params.InstType
	if instType == "" {
		instType = "SPOT"
	}

	// 构建任务参数（Intervals只包含单个interval）
	taskParams := TaskParams{
		DataType:   rule.DataType,
		DataSource: rule.DataSource,
		InstType:   instType,
		Symbol:     symbol,
		Intervals:  []string{interval}, // 只包含单个interval
	}

	data, err := json.Marshal(taskParams)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

// GetMatchingNodes 获取匹配的节点列表
func (d *KlinePlanner) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
