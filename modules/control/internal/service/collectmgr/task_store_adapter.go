package collectmgr

import (
	collectmgrtypes "github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/types"
)

// TaskStoreAdapter 任务实例仓库适配器
// 将 TaskInstanceStore 适配为 cloudnode.TaskInstanceStoreGetter 接口
// 用于解决循环依赖问题
type TaskStoreAdapter struct {
	store TaskInstanceStore
}

// NewTaskStoreAdapter 创建任务实例仓库适配器
func NewTaskStoreAdapter(store TaskInstanceStore) *TaskStoreAdapter {
	return &TaskStoreAdapter{store: store}
}

// GetByNodeID 实现 cloudnode.TaskInstanceStoreGetter 接口
func (a *TaskStoreAdapter) GetByNodeID(nodeID string) []collectmgrtypes.TaskInstanceLite {
	instances := a.store.GetByNodeID(nodeID)

	// 转换为轻量级结构
	result := make([]collectmgrtypes.TaskInstanceLite, 0, len(instances))
	for _, inst := range instances {
		result = append(result, collectmgrtypes.TaskInstanceLite{
			ID:              inst.ID,
			TaskID:          inst.TaskID,
			RuleID:          inst.RuleID,
			PlannedExecNode: inst.PlannedExecNode,
			DataType:        inst.CollectDataType,
			Symbol:          inst.Symbol,
			Interval:        inst.Interval,
			TaskParams:      inst.TaskParams,
			Invalid:         inst.Invalid,
		})
	}

	return result
}

// GetCount 实现 cloudnode.TaskInstanceStoreGetter 接口
func (a *TaskStoreAdapter) GetCount() int {
	return a.store.GetCount()
}
