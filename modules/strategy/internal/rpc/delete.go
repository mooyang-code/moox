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
	// CheckBoundTask 只做 DeleteBoundTask 动手前的全部检查（关联、补算、结果数据集能否删），不做修改；被拒绝时返回 *input.RejectedError。
	CheckBoundTask(ctx context.Context, taskID, instanceID string) error
	// DeleteBoundTask 删除关联到实例的任务，连同成员与结果数据集；任务不存在也返回成功；被拒绝时返回 *input.RejectedError。
	DeleteBoundTask(ctx context.Context, taskID, instanceID string) error
}

// ArchiveTasks 查询空间内有归档任务的数据集（archive-mgr 只读接口）。
type ArchiveTasks interface {
	ArchivedDatasets(ctx context.Context, spaceID string) (map[string]bool, error)
}

// errTaskDeleted 是启用“删除到一半”的实例时的报错：关联过的因子任务已删、实例还在。
const errTaskDeleted = "绑定的因子任务已删除，请重新执行删除"

// deleteCallReserve 是删除实例给最后那个事务留的时间：调用 Factor、archive-mgr 的截止时间比请求的截止时间提前这么多。
// 请求没有截止时间时，依赖调用最多用 deleteCallBudget。
const (
	deleteCallReserve = 5 * time.Second
	deleteCallBudget  = 25 * time.Second
)

// pendingTask 是删除实例时要删的一个因子任务。
type pendingTask struct {
	TaskID          string
	ResultDatasetID string
}

// taskPlan 是实例登记的因子任务分组：Delete 是绑定到本实例、要一起删除的；Keep 是没绑定或绑定到别的实例的
// （例如公共任务），保留不动，只去掉本实例的引用；Gone 是 Factor 里已经没有的（上次删除时已删掉，或被另外删掉）。
type taskPlan struct {
	Delete []pendingTask
	Keep   []string
	Gone   []string
}

func (p taskPlan) deleteIDs() []string {
	ids := make([]string, 0, len(p.Delete))
	for _, task := range p.Delete {
		ids = append(ids, task.TaskID)
	}
	return ids
}

// DeleteStrategyInstance 删除已停用、没有会话的实例，连同绑定到它的因子任务与结果数据集，最后在一个事务里删掉实例在策略库里的全部记录。
// 没绑定或绑定到别的实例的任务保留不动，只去掉本实例的引用。全程持有实例锁，并发的启用会被挡住。
// 所有拒绝（任务不在本空间、有回放在进行、Factor 的删除前检查不过、结果数据集有归档任务）都在删第一个任务之前做完；
// 已经删掉的部分算成功，中途失败（例如超时）后再删一次即可接着做完。Trade 里的影子目标、目标回执与订单不删。
func (s *Service) DeleteStrategyInstance(ctx context.Context, req *strategypb.DeleteStrategyInstanceReq) (*strategypb.DeleteStrategyInstanceRsp, error) {
	refuse := func(info *commonpb.RetInfo) (*strategypb.DeleteStrategyInstanceRsp, error) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: info}, nil
	}
	if err := s.ready(); err != nil {
		return refuse(failure(err))
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return refuse(invalid(err))
	}
	instanceID := strings.TrimSpace(req.GetInstanceId())
	if instanceID == "" {
		return refuse(invalid(errors.New("instance_id 不能为空")))
	}
	if s.Tasks == nil || s.Archives == nil {
		return refuse(invalid(errors.New("未接线 Factor 或归档服务，不能删除实例")))
	}
	instance, unlock, err := s.lockInstance(ctx, instanceID)
	if errors.Is(err, store.ErrNotFound) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
	}
	if err != nil {
		return refuse(failure(err))
	}
	defer unlock()
	if instance.SpaceID != scoped {
		return refuse(invalid(errors.New("实例不在当前空间")))
	}
	if instance.Enabled {
		return refuse(invalid(errors.New("实例必须先停用才能删除")))
	}
	if instance.SessionID != nil {
		return refuse(invalid(errors.New("实例的启用或停用操作尚未完成，稍后再删除")))
	}
	callCtx, cancel := deleteCallContext(ctx)
	defer cancel()
	plan, err := s.planTasks(callCtx, instance)
	if err != nil {
		return refuse(deleteError(err))
	}
	if err := s.checkNoActiveReplays(ctx, instance, plan); err != nil {
		return refuse(deleteError(err))
	}
	for _, task := range plan.Delete {
		if err := s.Tasks.CheckBoundTask(callCtx, task.TaskID, instance.InstanceID); err != nil {
			return refuse(deleteError(fmt.Errorf("因子任务 %s 暂不能删除：%w", task.TaskID, err)))
		}
	}
	if err := s.checkNotArchived(callCtx, instance.SpaceID, plan.Delete); err != nil {
		return refuse(deleteError(err))
	}
	// 从这里开始动手：之后的失败都标为“删除未完成”，再删一次即可接着做完。
	incomplete := func(info *commonpb.RetInfo) (*strategypb.DeleteStrategyInstanceRsp, error) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: info, Incomplete: true}, nil
	}
	for i, task := range plan.Delete {
		if err := s.Tasks.DeleteBoundTask(callCtx, task.TaskID, instance.InstanceID); err != nil {
			wrapped := fmt.Errorf("删除因子任务 %s 未完成（实例还在，可以重新执行删除）：%w", task.TaskID, err)
			var rejected *input.RejectedError
			if errors.As(err, &rejected) && i == 0 {
				// 检查之后才出现的拒绝（例如刚有人发起了补算），第一个任务被拒绝时还什么都没删。
				return refuse(invalid(fmt.Errorf("因子任务 %s 暂不能删除：%w", task.TaskID, err)))
			}
			if errors.As(err, &rejected) {
				return incomplete(invalid(wrapped))
			}
			return incomplete(failure(wrapped))
		}
	}
	// 年龄探针按 View 存：删掉的任务与已经不在的任务清掉它们的 View；保留的任务的 View 还在用，不动。
	cleared := append(plan.deleteIDs(), plan.Gone...)
	viewIDs := make([]string, 0, len(cleared))
	for _, taskID := range cleared {
		if viewID, err := input.ViewIDOfTask(taskID); err == nil {
			viewIDs = append(viewIDs, viewID)
		}
	}
	if err := s.Store.DeleteInstance(ctx, instance.InstanceID, instance.SpaceID, viewIDs); err != nil && !errors.Is(err, store.ErrNotFound) {
		return incomplete(failure(fmt.Errorf("因子任务已删除，删除实例记录未完成（可以重新执行删除）：%w", err)))
	}
	return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
}

// deleteCallContext 是调用 Factor、archive-mgr 用的 ctx：截止时间比请求提前 deleteCallReserve，给最后那个事务留出时间。
func deleteCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(deleteCallBudget)
	if requested, ok := ctx.Deadline(); ok && requested.Add(-deleteCallReserve).Before(deadline) {
		deadline = requested.Add(-deleteCallReserve)
	}
	return context.WithDeadline(ctx, deadline)
}

// rejection 是删除前核对不通过：请求本身不能执行（区别于暂时读不到依赖）。
type rejection struct{ error }

// deleteError 把删除前的核对结果转成接口错误：拒绝（含 Factor 的明确拒绝）按参数错误返回，读不到依赖按内部错误返回。
func deleteError(err error) *commonpb.RetInfo {
	var rejected rejection
	var factorRejected *input.RejectedError
	if errors.As(err, &rejected) || errors.As(err, &factorRejected) {
		return invalid(err)
	}
	return failure(err)
}

// checkNoActiveReplays 拒绝删除还有回放在读它数据的实例：从实例发起的回放，以及本空间里读取要删的任务的回放（按定义或 DSL 发起的）。
func (s *Service) checkNoActiveReplays(ctx context.Context, instance store.Instance, plan taskPlan) error {
	replays, err := s.Store.ActiveReplaysOfInstance(ctx, instance.InstanceID, instance.SpaceID, plan.deleteIDs())
	if err != nil {
		return fmt.Errorf("读取进行中的回放失败，暂不能删除：%w", err)
	}
	if len(replays) > 0 {
		return rejection{fmt.Errorf("有回放正在读取这个实例的数据（%s），请先取消回放，再删除实例", strings.Join(replays, "、"))}
	}
	return nil
}

// planTasks 逐个读实例登记的因子任务，按绑定分组：只有绑定到本实例的任务随实例删除；要删的任务必须与实例在同一个空间。
func (s *Service) planTasks(ctx context.Context, instance store.Instance) (taskPlan, error) {
	var plan taskPlan
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		task, err := s.Tasks.GetFactorTask(ctx, taskID)
		if errors.Is(err, input.ErrFactorTaskNotFound) {
			plan.Gone = append(plan.Gone, taskID)
			continue
		}
		if err != nil {
			return taskPlan{}, fmt.Errorf("读取因子任务 %s 失败，暂不能删除：%w", taskID, err)
		}
		if task.BoundInstanceID != instance.InstanceID {
			plan.Keep = append(plan.Keep, taskID)
			continue
		}
		if task.SpaceID != instance.SpaceID {
			return taskPlan{}, rejection{fmt.Errorf("因子任务 %s 属于空间 %s，不是实例所在的空间 %s，不能随实例删除", taskID, task.SpaceID, instance.SpaceID)}
		}
		plan.Delete = append(plan.Delete, pendingTask{TaskID: taskID, ResultDatasetID: task.ResultDatasetID})
	}
	return plan, nil
}

// checkNotArchived 拒绝删除有归档任务的结果数据集：archive-mgr 不能按数据集过滤，分页拉回空间内全部归档任务后在本地筛选。
// 归档任务按（空间, 数据集）标识，列出时写成 空间/数据集。
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
			blocked = append(blocked, spaceID+"/"+task.ResultDatasetID)
		}
	}
	if len(blocked) > 0 {
		sort.Strings(blocked)
		return rejection{fmt.Errorf("结果数据集有归档任务 %s，请先到归档页删除这些归档任务，再删除实例", strings.Join(blocked, "、"))}
	}
	return nil
}

// boundTasks 返回关联过本实例的因子任务：当前与历次会话固化的绑定解析里出现的任务（关联发生在写会话之前）。
func (s *Service) boundTasks(ctx context.Context, instance store.Instance) (map[string]bool, error) {
	resolvedJSONs, err := s.Store.SessionResolvedJSONs(ctx, instance.InstanceID)
	if err != nil {
		return nil, err
	}
	bound := map[string]bool{}
	for _, raw := range append(resolvedJSONs, string(instance.ResolvedJSON)) {
		resolved, err := input.ParseResolved([]byte(raw))
		if err != nil {
			continue
		}
		for _, task := range resolved.Tasks {
			bound[task.FactorTaskID] = true
		}
	}
	return bound, nil
}

// checkTasksExist 在启用新建会话之前确认登记的因子任务都还在：删除实例做到一半（任务已删、实例还在）时直接给出明确的报错，
// 不让解析把“View 不存在”当成“还在创建”一直重试。未接线时不检查。
func (s *Service) checkTasksExist(ctx context.Context, instance store.Instance) error {
	if s.Tasks == nil {
		return nil
	}
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		_, err := s.Tasks.GetFactorTask(ctx, taskID)
		if err != nil && !errors.Is(err, input.ErrFactorTaskNotFound) {
			return err
		}
		if err == nil {
			continue
		}
		bound, boundErr := s.boundTasks(ctx, instance)
		if boundErr == nil && bound[taskID] {
			return fmt.Errorf("因子任务 %s 不存在：%s", taskID, errTaskDeleted)
		}
		return fmt.Errorf("引用的因子任务 %s 已不存在", taskID)
	}
	return nil
}

// missingTasksBudget 是列表与详情里检查因子任务还在不在的时间预算：列表与详情的路由超时只有 5 秒，Factor 慢时宁可不标，
// 也不能拖到超时报错。
const missingTasksBudget = 1500 * time.Millisecond

// markMissingTasks 给停用实例标出已在 Factor 删除的因子任务：关联过本实例的记为删除未完成（missing_factor_tasks），
// 从没关联过的（被另外删掉）记为不存在（absent_factor_tasks）。perTask 为真（详情）时逐个读任务，否则（列表）列一次全部任务；
// 读不到 Factor 或超出预算时不标，只写日志，不让列表和详情因此报错。
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
		bound, err := s.boundTasks(ctx, instances[i])
		if err != nil {
			log.Warnf("读取实例 %s 的会话失败，本次不标出删除未完成：%v", instances[i].InstanceID, err)
			continue
		}
		for _, taskID := range taskIDs {
			if bound[taskID] {
				items[i].MissingFactorTasks = append(items[i].MissingFactorTasks, taskID)
			} else {
				items[i].AbsentFactorTasks = append(items[i].AbsentFactorTasks, taskID)
			}
		}
	}
}
