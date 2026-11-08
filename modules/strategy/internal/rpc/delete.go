package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// FactorTaskAdmin 是删除实例、启用前核对与标出“删除未完成”用到的 FactorMgr 接口。
type FactorTaskAdmin interface {
	// GetFactorTask 读取任务；任务不存在时返回 input.ErrFactorTaskNotFound。
	GetFactorTask(ctx context.Context, taskID string) (input.FactorTaskRecord, error)
	// ListFactorTaskIDs 列出 Factor 里现有的全部任务 ID。
	ListFactorTaskIDs(ctx context.Context) (map[string]bool, error)
	// DeleteBoundTask 删除关联到实例的任务，连同成员与结果数据集；任务不存在也返回成功。
	DeleteBoundTask(ctx context.Context, taskID, instanceID string) error
}

// ArchiveTasks 查询空间内有归档任务的数据集（archive-mgr 只读接口）。
type ArchiveTasks interface {
	ArchivedDatasets(ctx context.Context, spaceID string) (map[string]bool, error)
}

// errTaskDeleted 是启用“删除到一半”的实例时的报错：因子任务已删、实例还在。
const errTaskDeleted = "绑定的因子任务已删除，请重新执行删除"

// pendingTask 是删除实例时还要删的一个因子任务。
type pendingTask struct {
	TaskID          string
	ResultDatasetID string
}

// DeleteStrategyInstance 删除已停用、没有会话的实例，连同它关联的因子任务与结果数据集，最后在一个事务里删掉实例在策略库里的全部记录。
// 全程持有实例锁，并发的启用会被挡住。所有拒绝（关联的不是本实例、结果数据集有归档任务）都在删第一个任务之前做完；
// 已经删掉的部分算成功，中途失败（例如超时）后再删一次即可接着做完。Trade 里的影子目标、目标回执与订单不删。
func (s *Service) DeleteStrategyInstance(ctx context.Context, req *strategypb.DeleteStrategyInstanceReq) (*strategypb.DeleteStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	instanceID := strings.TrimSpace(req.GetInstanceId())
	if instanceID == "" {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	if s.Tasks == nil || s.Archives == nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("未接线 Factor 或归档服务，不能删除实例"))}, nil
	}
	instance, unlock, err := s.lockInstance(ctx, instanceID)
	if errors.Is(err, store.ErrNotFound) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
	}
	if err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	defer unlock()
	if instance.SpaceID != scoped {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	if instance.Enabled {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("实例必须先停用才能删除"))}, nil
	}
	if instance.SessionID != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("实例的启用或停用操作尚未完成，稍后再删除"))}, nil
	}
	pending, err := s.pendingTasks(ctx, instance)
	if err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: deleteError(err)}, nil
	}
	if err := s.checkNotArchived(ctx, instance.SpaceID, pending); err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: deleteError(err)}, nil
	}
	for _, task := range pending {
		if err := s.Tasks.DeleteBoundTask(ctx, task.TaskID, instance.InstanceID); err != nil {
			return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(fmt.Errorf("删除因子任务 %s 未完成（实例还在，可以重新执行删除）：%w", task.TaskID, err))}, nil
		}
	}
	viewIDs := make([]string, 0, len(instance.FactorTasks))
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		if viewID, err := input.ViewIDOfTask(taskID); err == nil {
			viewIDs = append(viewIDs, viewID)
		}
	}
	if err := s.Store.DeleteInstance(ctx, instance.InstanceID, instance.SpaceID, viewIDs); err != nil && !errors.Is(err, store.ErrNotFound) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(fmt.Errorf("因子任务已删除，删除实例记录未完成（可以重新执行删除）：%w", err))}, nil
	}
	return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
}

// rejection 是删除前核对不通过：请求本身不能执行（区别于暂时读不到依赖）。
type rejection struct{ error }

// deleteError 把删除前的核对结果转成接口错误：拒绝按参数错误返回，读不到依赖按内部错误返回。
func deleteError(err error) *commonpb.RetInfo {
	var rejected rejection
	if errors.As(err, &rejected) {
		return invalid(err)
	}
	return failure(err)
}

// pendingTasks 读取实例登记的每个因子任务：已经删掉的跳过；关联的不是本实例（包括实例从未启用、任务还没有关联）的拒绝。
func (s *Service) pendingTasks(ctx context.Context, instance store.Instance) ([]pendingTask, error) {
	var pending []pendingTask
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		task, err := s.Tasks.GetFactorTask(ctx, taskID)
		if errors.Is(err, input.ErrFactorTaskNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取因子任务 %s 失败，暂不能删除：%w", taskID, err)
		}
		switch task.BoundInstanceID {
		case instance.InstanceID:
		case "":
			return nil, rejection{fmt.Errorf("因子任务 %s 还没有关联本实例（实例从未启用过），不能随实例删除；先在因子任务页停用并删除这个任务（选“同时清理结果数据集”），再重新删除实例", taskID)}
		default:
			return nil, rejection{fmt.Errorf("因子任务 %s 关联的是实例 %s，不是本实例，不能随本实例删除", taskID, task.BoundInstanceID)}
		}
		pending = append(pending, pendingTask{TaskID: taskID, ResultDatasetID: task.ResultDatasetID})
	}
	return pending, nil
}

// checkNotArchived 拒绝删除有归档任务的结果数据集：archive-mgr 不能按数据集过滤，分页拉回空间内全部归档任务后在本地筛选。
func (s *Service) checkNotArchived(ctx context.Context, spaceID string, pending []pendingTask) error {
	if len(pending) == 0 {
		return nil
	}
	archived, err := s.Archives.ArchivedDatasets(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("读不到归档任务，暂不能删除（删除前要确认结果数据集没有归档任务）：%w", err)
	}
	var blocked []string
	for _, task := range pending {
		if archived[task.ResultDatasetID] {
			blocked = append(blocked, task.ResultDatasetID)
		}
	}
	if len(blocked) > 0 {
		sort.Strings(blocked)
		return rejection{fmt.Errorf("结果数据集 %s 有归档任务，请先到归档页删除这些归档任务，再删除实例", strings.Join(blocked, "、"))}
	}
	return nil
}

// checkTasksExist 在启用新建会话之前确认登记的因子任务都还在：删除实例做到一半（任务已删、实例还在）时直接给出明确的报错，
// 不让解析把“View 不存在”当成“还在创建”一直重试。未接线时不检查。
func (s *Service) checkTasksExist(ctx context.Context, instance store.Instance) error {
	if s.Tasks == nil {
		return nil
	}
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		if _, err := s.Tasks.GetFactorTask(ctx, taskID); errors.Is(err, input.ErrFactorTaskNotFound) {
			return fmt.Errorf("因子任务 %s 不存在：%s", taskID, errTaskDeleted)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// missingTasksBudget 是列表与详情里检查因子任务还在不在的时间预算：列表与详情的路由超时只有 5 秒，Factor 慢时宁可不标，
// 也不能拖到超时报错。
const missingTasksBudget = 1500 * time.Millisecond

// markMissingTasks 给停用实例标出已在 Factor 删除的因子任务（删除未完成）。perTask 为真（详情）时逐个读任务，否则（列表）
// 列一次全部任务；读不到 Factor 或超出预算时不标，只写日志，不让列表和详情因此报错。
func (s *Service) markMissingTasks(ctx context.Context, instances []store.Instance, items []*strategypb.StrategyInstance, perTask bool) {
	if s.Tasks == nil {
		return
	}
	var checked []int
	for i, instance := range instances {
		if !instance.Enabled && len(instance.FactorTasks) > 0 {
			checked = append(checked, i)
		}
	}
	if len(checked) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, missingTasksBudget)
	defer cancel()
	var exists func(taskID string) (bool, error)
	if perTask {
		exists = func(taskID string) (bool, error) {
			_, err := s.Tasks.GetFactorTask(ctx, taskID)
			if errors.Is(err, input.ErrFactorTaskNotFound) {
				return false, nil
			}
			return err == nil, err
		}
	} else {
		existing, err := s.Tasks.ListFactorTaskIDs(ctx)
		if err != nil {
			log.Warnf("检查实例的因子任务是否还在失败，本次不标出删除未完成的实例：%v", input.RawCause(err))
			return
		}
		exists = func(taskID string) (bool, error) { return existing[taskID], nil }
	}
	missing := make(map[int][]string, len(checked))
	for _, i := range checked {
		for _, taskID := range input.TaskIDs(instances[i].FactorTasks) {
			found, err := exists(taskID)
			if err != nil {
				log.Warnf("检查实例 %s 的因子任务 %s 是否还在失败，本次不标出删除未完成：%v", instances[i].InstanceID, taskID, input.RawCause(err))
				return
			}
			if !found {
				missing[i] = append(missing[i], taskID)
			}
		}
	}
	for i, taskIDs := range missing {
		items[i].MissingFactorTasks = taskIDs
	}
}
