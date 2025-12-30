package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
	"trpc.group/trpc-go/trpc-go/log"
)

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

// GetTargetObjects 获取目标对象列表（交易对）
// 支持通配符匹配:
//   - "*" 表示全部对象
//   - "BTC-*" 匹配所有以 BTC- 开头的交易对
//   - "*-USDT" 匹配所有以 -USDT 结尾的交易对
func (d *KlinePlanner) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
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
	log.InfoContextf(ctx, "DataSource:%+v; params.Objects:%+v; allSymbols : %+v",
		rule.DataSource, params.Objects, allSymbols)

	// 3. 如果规则没有指定 objects，返回所有可用标的
	if len(params.Objects) == 0 {
		return allSymbols, nil
	}

	// 4. 解析通配符模式，返回匹配的对象
	return ResolveObjectPatterns(params.Objects, allSymbols), nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *KlinePlanner) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
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
		Intervals:  params.Intervals,
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
