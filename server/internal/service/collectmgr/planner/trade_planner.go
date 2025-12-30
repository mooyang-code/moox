package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// TradePlanner Trade规划器
type TradePlanner struct {
	base *BasePlanner
}

// NewTradePlanner 创建Trade规划器
func NewTradePlanner(base *BasePlanner) *TradePlanner {
	return &TradePlanner{base: base}
}

// GetDataType 返回支持的数据类型
func (d *TradePlanner) GetDataType() string {
	return model.DataTypeTrade
}

// GetTargetObjects 获取目标对象列表（交易对）
// 支持通配符匹配:
//   - "*" 表示全部对象
//   - "BTC-*" 匹配所有以 BTC- 开头的交易对
//   - "*-USDT" 匹配所有以 -USDT 结尾的交易对
func (d *TradePlanner) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	// 1. 从规则参数解析 objects（可能包含通配符）
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return nil, err
	}

	// 2. 从 SymbolProvider 获取所有可用标的
	allSymbols, err := d.base.GetSymbolProvider().GetSymbols(ctx, rule.DataSource)
	if err != nil {
		allSymbols = []string{}
	}

	// 3. 如果规则没有指定 objects，返回所有可用标的
	if len(params.Objects) == 0 {
		return allSymbols, nil
	}

	// 4. 解析通配符模式，返回匹配的对象
	return ResolveObjectPatterns(params.Objects, allSymbols), nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *TradePlanner) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
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
func (d *TradePlanner) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
