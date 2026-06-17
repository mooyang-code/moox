package collectmgr

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"

	cmap "github.com/orcaman/concurrent-map/v2"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskInstanceStore 内存任务实例仓库（线程安全）
// 作为系统唯一事实来源，所有任务查询都从内存进行
// 每次重算时全量覆盖，不保留历史状态
type TaskInstanceStore interface {
	// ReplaceAll 原子替换全部实例（重算后调用）
	ReplaceAll(ctx context.Context, instances []*model.CollectorTaskInstance)

	// GetByNodeID 按节点ID查询任务实例（查询 PlannedExecNode）
	GetByNodeID(nodeID string) []*model.CollectorTaskInstance

	// GetByTaskID 按TaskID查询任务实例
	GetByTaskID(taskID string) *model.CollectorTaskInstance

	// UpdateStatusWithNode 更新单个实例状态（心跳上报时，新增 nodeID 参数）
	UpdateStatusWithNode(taskID string, nodeID string, status int, lastExecTime *time.Time, result string)

	// GetSnapshot 获取全部实例快照（刷库时）
	GetSnapshot() []*model.CollectorTaskInstance

	// GetVersion 获取内存版本号（用于MD5缓存优化）
	GetVersion() uint64

	// GetCount 获取任务实例总数
	GetCount() int
}

// taskInstanceStoreImpl 内存任务实例仓库实现
type taskInstanceStoreImpl struct {
	// 使用 concurrent-map 存储任务实例
	// Key: TaskID, Value: *model.CollectorTaskInstance
	store cmap.ConcurrentMap[string, *model.CollectorTaskInstance]

	// 版本号，每次ReplaceAll或UpdateStatus时自增
	// 用于MD5缓存失效判定
	version atomic.Uint64
}

// NewTaskInstanceStore 创建内存任务实例仓库
func NewTaskInstanceStore() TaskInstanceStore {
	return &taskInstanceStoreImpl{
		store: cmap.New[*model.CollectorTaskInstance](),
	}
}

// ReplaceAll 原子替换全部实例
// 重算完成后调用，全量覆盖写入，保留任务最后一次执行信息
func (s *taskInstanceStoreImpl) ReplaceAll(ctx context.Context, instances []*model.CollectorTaskInstance) {
	// 1. 缓存旧实例的最后执行信息
	type lastExecSnapshot struct {
		lastExecNode   string
		lastExecStatus int
		lastExecTime   *time.Time
		result         string
	}
	oldSnapshot := make(map[string]lastExecSnapshot, s.store.Count())
	s.store.IterCb(func(key string, inst *model.CollectorTaskInstance) {
		if inst == nil {
			return
		}
		oldSnapshot[key] = lastExecSnapshot{
			lastExecNode:   inst.LastExecNode,
			lastExecStatus: inst.LastExecStatus,
			lastExecTime:   inst.LastExecTime,
			result:         inst.Result,
		}
	})

	// 2. 清空现有数据
	s.store.Clear()

	// 3. 插入新数据（保留历史执行信息）
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if snapshot, ok := oldSnapshot[inst.TaskID]; ok {
			inst.LastExecNode = snapshot.lastExecNode
			inst.LastExecStatus = snapshot.lastExecStatus
			inst.LastExecTime = snapshot.lastExecTime
			inst.Result = snapshot.result
		}
		s.store.Set(inst.TaskID, inst)
	}

	// 4. 版本号自增
	s.version.Add(1)

	log.InfoContextf(ctx, "[TaskInstanceStore] ReplaceAll completed: total=%d, version=%d",
		len(instances), s.version.Load())
}

// GetByNodeID 按节点ID查询任务实例（查询 PlannedExecNode）
func (s *taskInstanceStoreImpl) GetByNodeID(nodeID string) []*model.CollectorTaskInstance {
	var result []*model.CollectorTaskInstance

	// 遍历所有实例，过滤出属于该节点的任务
	s.store.IterCb(func(key string, inst *model.CollectorTaskInstance) {
		if inst.PlannedExecNode == nodeID && inst.Invalid == model.InvalidNo {
			result = append(result, inst)
		}
	})

	return result
}

// GetByTaskID 按TaskID查询任务实例
func (s *taskInstanceStoreImpl) GetByTaskID(taskID string) *model.CollectorTaskInstance {
	inst, ok := s.store.Get(taskID)
	if !ok {
		return nil
	}
	return inst
}

// UpdateStatusWithNode 更新单个实例状态（心跳上报时调用，新增 nodeID 参数）
// 仅更新内存，不写DB
func (s *taskInstanceStoreImpl) UpdateStatusWithNode(taskID string, nodeID string, status int, lastExecTime *time.Time, result string) {
	inst, ok := s.store.Get(taskID)
	if !ok {
		log.Warnf("[TaskInstanceStore] UpdateStatusWithNode failed: taskID=%s not found", taskID)
		return
	}

	// 更新增字段
	inst.LastExecNode = nodeID
	inst.LastExecStatus = status
	inst.LastExecTime = lastExecTime
	inst.Result = result
	inst.ModifyTime = time.Now()

	// 版本号自增（触发MD5缓存失效）
	s.version.Add(1)

	log.Debugf("[TaskInstanceStore] UpdateStatusWithNode: taskID=%s, nodeID=%s, status=%d, version=%d",
		taskID, nodeID, status, s.version.Load())
}

// GetSnapshot 获取全部实例快照
// 刷库时调用，返回所有实例的副本
func (s *taskInstanceStoreImpl) GetSnapshot() []*model.CollectorTaskInstance {
	var snapshot []*model.CollectorTaskInstance

	s.store.IterCb(func(key string, inst *model.CollectorTaskInstance) {
		// 创建副本（避免并发修改）
		snapshot = append(snapshot, &model.CollectorTaskInstance{
			ID:              inst.ID,
			TaskID:          inst.TaskID,
			RuleID:          inst.RuleID,
			PlannedExecNode: inst.PlannedExecNode,
			LastExecNode:    inst.LastExecNode,
			LastExecStatus:  inst.LastExecStatus,
			Symbol:          inst.Symbol,
			CollectDataType: inst.CollectDataType,
			Interval:        inst.Interval,
			TaskParams:      inst.TaskParams,
			LastExecTime:    inst.LastExecTime,
			Result:          inst.Result,
			Invalid:         inst.Invalid,
			CreateTime:      inst.CreateTime,
			ModifyTime:      inst.ModifyTime,
		})
	})

	return snapshot
}

// GetVersion 获取内存版本号
func (s *taskInstanceStoreImpl) GetVersion() uint64 {
	return s.version.Load()
}

// GetCount 获取任务实例总数
func (s *taskInstanceStoreImpl) GetCount() int {
	return s.store.Count()
}
