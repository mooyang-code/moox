package planner

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
)

// NewsPlanner 新闻规划器
type NewsPlanner struct {
	base *BasePlanner
}

// NewNewsPlanner 创建新闻规划器
func NewNewsPlanner(base *BasePlanner) *NewsPlanner {
	return &NewsPlanner{base: base}
}

// GetDataType 返回支持的数据类型
func (d *NewsPlanner) GetDataType() string {
	return model.DataTypeNews
}

// GetTargetObjects 获取目标对象列表
// 新闻不按标的拆分，返回空数组
// 统一处理逻辑会生成一个 symbol="" 的实例
func (d *NewsPlanner) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	return nil, nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *NewsPlanner) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
	params, err := d.base.ParseCollectParams(rule.CollectParams)
	if err != nil {
		return "{}", err
	}

	taskParams := TaskParams{
		DataType:   rule.DataType,
		DataSource: rule.DataSource,
		Sources:    params.Sources,
		Keywords:   params.Keywords,
	}

	data, err := json.Marshal(taskParams)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

// GetMatchingNodes 获取匹配的节点列表
func (d *NewsPlanner) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
