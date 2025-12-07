package distributor

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// TradeDistributor Trade分配器
type TradeDistributor struct {
	base *BaseDistributor
}

// NewTradeDistributor 创建Trade分配器
func NewTradeDistributor(base *BaseDistributor) *TradeDistributor {
	return &TradeDistributor{base: base}
}

// GetDataType 返回支持的数据类型
func (d *TradeDistributor) GetDataType() string {
	return model.DataTypeTrade
}

// GetTargetObjects 获取目标对象列表（交易对）
func (d *TradeDistributor) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	// 1. 从规则参数解析 objects
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return nil, err
	}
	objectsFromRule := params.Objects

	// 2. 从 SymbolProvider 获取动态标的（可选）
	objectsFromProvider, err := d.base.GetSymbolProvider().GetSymbols(ctx, rule.DataSource)
	if err != nil {
		objectsFromProvider = []string{}
	}

	// 3. 合并去重
	return MergeUnique(objectsFromRule, objectsFromProvider), nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *TradeDistributor) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return "{}", err
	}

	taskParams := TaskParams{
		Symbol:     object,
		Limit:      params.Limit,
		DataSource: rule.DataSource,
	}

	data, err := json.Marshal(taskParams)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

// GetMatchingNodes 获取匹配的节点列表
func (d *TradeDistributor) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
