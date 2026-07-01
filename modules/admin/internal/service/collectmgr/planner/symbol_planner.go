package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// SymbolPlanner 标的同步规划器
type SymbolPlanner struct {
	base *BasePlanner
}

// NewSymbolPlanner 创建标的同步规划器
func NewSymbolPlanner(base *BasePlanner) *SymbolPlanner {
	return &SymbolPlanner{base: base}
}

// GetDataType 返回支持的数据类型
func (d *SymbolPlanner) GetDataType() string {
	return model.DataTypeSymbol
}

// GetTargetObjects 获取目标对象列表（产品类型）
// Symbol 任务不按标的拆分，而是按产品类型拆分
// 返回: ["SPOT", "SWAP"] 或 ["SPOT"] 等
func (d *SymbolPlanner) GetTargetObjects(ctx context.Context, rule *model.CollectorTaskRules) ([]string, error) {
	log.DebugContextf(ctx, "[SymbolPlanner] GetTargetObjects enter (ruleID=%s, dataSource=%s)",
		rule.RuleID, rule.DataSource)

	// 1. 从规则参数解析 inst_types
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		log.ErrorContextf(ctx, "[SymbolPlanner] Failed to parse collect params (ruleID=%s): %v",
			rule.RuleID, err)
		return nil, err
	}

	// 2. 如果没有指定 inst_types，默认返回 SPOT
	if len(params.InstTypes) == 0 {
		log.InfoContextf(ctx, "[SymbolPlanner] No inst_types specified, using default [SPOT] (ruleID=%s)",
			rule.RuleID)
		return []string{"SPOT"}, nil
	}

	// 3. 返回配置的产品类型列表
	log.InfoContextf(ctx, "[SymbolPlanner] Got %d inst_types: %v (ruleID=%s)",
		len(params.InstTypes), params.InstTypes, rule.RuleID)
	return params.InstTypes, nil
}

// BuildTaskParams 为指定产品类型构建任务参数
// object 参数是产品类型（如 "SPOT", "SWAP"）
func (d *SymbolPlanner) BuildTaskParams(ctx context.Context, rule *model.CollectorTaskRules, object string) (string, error) {
	// 解析采集参数以获取 intervals
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return "{}", err
	}
	intervals := params.Intervals
	if len(intervals) == 0 {
		// Symbol 同步不是 K 线任务，但仍需要一个调度周期让 SCF 定期执行。
		// 默认每天刷新一次，避免 default 周期被 executor 跳过，也避免每次心跳都拉全量交易对。
		intervals = []string{"1d"}
	}

	// Symbol 任务的 object 是产品类型（SPOT/SWAP/FUTURES）
	taskParams := TaskParams{
		DataType:   rule.DataType,
		DataSource: rule.DataSource,
		InstType:   object,    // SPOT, SWAP, FUTURES
		Symbol:     "",        // Symbol 任务不指定具体标的
		Intervals:  intervals, // 时间周期
	}

	data, err := json.Marshal(taskParams)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

// GetMatchingNodes 获取匹配的节点列表
func (d *SymbolPlanner) GetMatchingNodes(ctx context.Context, rule *model.CollectorTaskRules) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
