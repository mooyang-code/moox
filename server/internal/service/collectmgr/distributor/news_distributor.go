package distributor

import (
	"context"
	"encoding/json"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// NewsDistributor 新闻分配器
type NewsDistributor struct {
	base *BaseDistributor
}

// NewNewsDistributor 创建新闻分配器
func NewNewsDistributor(base *BaseDistributor) *NewsDistributor {
	return &NewsDistributor{base: base}
}

// GetDataType 返回支持的数据类型
func (d *NewsDistributor) GetDataType() string {
	return model.DataTypeNews
}

// GetTargetObjects 获取目标对象列表
// 新闻不按标的拆分，返回空数组
// 统一处理逻辑会生成一个 symbol="" 的实例
func (d *NewsDistributor) GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error) {
	return nil, nil
}

// BuildTaskParams 为指定对象构建任务参数
func (d *NewsDistributor) BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error) {
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
func (d *NewsDistributor) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error) {
	return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
