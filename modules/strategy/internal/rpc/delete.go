package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// FactorTaskAdmin 是删除实例、启用前核对与标出“删除未完成”用到的 FactorMgr 接口。
type FactorTaskAdmin interface {
	// GetFactorTask 读取任务；任务不存在时返回 input.ErrFactorTaskNotFound。
	GetFactorTask(ctx context.Context, taskID string) (upstream.FactorTaskRecord, error)
	// ListFactorTaskIDs 列出 Factor 里现有的全部任务 ID。
	ListFactorTaskIDs(ctx context.Context) (map[string]bool, error)
	// CheckBoundTask 只做 DeleteBoundTask 动手前的全部检查（关联、补算、结果数据集能否删），不做修改；被拒绝时返回 *input.RejectedError。
	CheckBoundTask(ctx context.Context, taskID, instanceID string) error
	// DeleteBoundTask 删除关联到实例的任务，连同成员与结果数据集；任务不存在也返回成功；被拒绝时返回 *input.RejectedError。
	DeleteBoundTask(ctx context.Context, taskID, instanceID string) error
}

// errTaskMissing 是实例引用的因子任务已不存在时的提示（删除实例做到一半，或任务被另外删掉）。
const errTaskMissing = "引用的因子任务已不存在，删除实例即可清理"

// deleteCallReserve 是删除实例给最后那个事务留的时间：调用 Factor 与 Trade 的截止时间比请求的截止时间提前这么多。
// 请求没有截止时间时，依赖调用最多用 deleteCallBudget。
const (
	deleteCallReserve = 5 * time.Second
	deleteCallBudget  = 25 * time.Second
)

// taskPlan 是实例登记的因子任务分组：Delete 是关联到本实例（声明了 factor_params 的实例启用时关联）、要一起删除的；
// Keep 是没关联或关联到别的实例的（不调参的实例引用的任务都在这里），保留不动，只去掉本实例的引用；
// Gone 是 Factor 里已经没有的（上次删除时已删掉，或被另外删掉）。
type taskPlan struct {
	Delete []string
	Keep   []string
	Gone   []string
}

// DeleteStrategyInstance 删除已停用的实例，连同关联到它的因子任务与结果数据集，最后在一个事务里删掉实例在策略库里的全部记录。
// 没关联或关联到别的实例的任务保留不动，只去掉本实例的引用；只有固定成员的任务从不关联，不随任何实例删除。全程持有实例锁，并发的启用会被挡住。
// 所有拒绝（任务不在本空间、要删的任务还被别的实例引用、有回放在进行、Factor 的删除前检查不过、Trade 没确认关闭会话）
// 都在删第一个任务之前做完；已经删掉的部分算成功，中途失败（例如超时）后再删一次即可接着做完。Trade 里的影子目标、目标回执与订单不删。
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
	if s.Tasks == nil {
		return refuse(invalid(errors.New("未接线 Factor 服务，不能删除实例")))
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
	callCtx, cancel := deleteCallContext(ctx)
	defer cancel()
	plan, err := s.planTasks(callCtx, instance)
	if err != nil {
		return refuse(deleteError(err))
	}
	if err := s.checkNoActiveReplays(ctx, instance, plan); err != nil {
		return refuse(deleteError(err))
	}
	for _, taskID := range plan.Delete {
		if err := s.Tasks.CheckBoundTask(callCtx, taskID, instance.InstanceID); err != nil {
			return refuse(deleteError(fmt.Errorf("因子任务 %s 暂不能删除：%w", taskID, err)))
		}
	}
	if err := s.closeBeforeDelete(callCtx, instance); err != nil {
		return refuse(deleteError(err))
	}
	// 从这里开始动手：之后的失败都标为“删除未完成”，再删一次即可接着做完。
	incomplete := func(info *commonpb.RetInfo) (*strategypb.DeleteStrategyInstanceRsp, error) {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: info, Incomplete: true}, nil
	}
	for i, taskID := range plan.Delete {
		if err := s.Tasks.DeleteBoundTask(callCtx, taskID, instance.InstanceID); err != nil {
			wrapped := fmt.Errorf("删除因子任务 %s 未完成（实例还在，可以重新执行删除）：%w", taskID, err)
			var rejected *upstream.RejectedError
			if errors.As(err, &rejected) && i == 0 {
				// 检查之后才出现的拒绝（例如刚有人发起了补算），第一个任务被拒绝时还什么都没删。
				return refuse(invalid(fmt.Errorf("因子任务 %s 暂不能删除：%w", taskID, err)))
			}
			if errors.As(err, &rejected) {
				return incomplete(invalid(wrapped))
			}
			return incomplete(failure(wrapped))
		}
	}
	// 年龄探针按 View 存：删掉的任务与已经不在的任务清掉它们的 View；保留的任务的 View 还在用，不动。
	cleared := append(slices.Clone(plan.Delete), plan.Gone...)
	datasetIDs := make([]string, 0, len(cleared))
	for _, taskID := range cleared {
		if datasetID, err := upstream.DatasetIDOfTask(taskID); err == nil {
			datasetIDs = append(datasetIDs, datasetID)
		}
	}
	if err := s.Store.DeleteInstance(ctx, instance.InstanceID, instance.SpaceID, datasetIDs); err != nil && !errors.Is(err, store.ErrNotFound) {
		return incomplete(failure(fmt.Errorf("因子任务已删除，删除实例记录未完成（可以重新执行删除）：%w", err)))
	}
	return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
}

// deleteCallContext 是调用 Factor 与 Trade 用的 ctx：截止时间比请求提前 deleteCallReserve，给最后那个事务留出时间。
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
	var factorRejected *upstream.RejectedError
	if errors.As(err, &rejected) || errors.As(err, &factorRejected) {
		return invalid(err)
	}
	return failure(err)
}

// checkNoActiveReplays 拒绝删除还有回放在读它数据的实例：从实例发起的回放，以及本空间里读取要删的任务的回放（按定义或 DSL 发起的）。
func (s *Service) checkNoActiveReplays(ctx context.Context, instance store.Instance, plan taskPlan) error {
	replays, err := s.Store.ActiveReplaysOfInstance(ctx, instance.InstanceID, instance.SpaceID, plan.Delete)
	if err != nil {
		return fmt.Errorf("读取进行中的回放失败，暂不能删除：%w", err)
	}
	if len(replays) > 0 {
		return rejection{fmt.Errorf("有回放正在读取这个实例的数据（%s），请先取消回放，再删除实例", strings.Join(replays, "、"))}
	}
	return nil
}

// planTasks 逐个读实例登记的因子任务，按关联分组：只有关联到本实例的任务随实例删除；要删的任务必须与实例在同一个空间，
// 并且不能还被别的实例引用——任务删了它们就读不到列；也不能只去掉本实例的引用把任务留下：留下的任务仍关联着已删的实例、
// 关联又不能改，Factor 的普通删除拒绝关联过的任务，这个任务就再也删不掉了。所以拒绝删除，列出引用方，先删它们。
func (s *Service) planTasks(ctx context.Context, instance store.Instance) (taskPlan, error) {
	var plan taskPlan
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		task, err := s.Tasks.GetFactorTask(ctx, taskID)
		if errors.Is(err, upstream.ErrFactorTaskNotFound) {
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
		referrers, err := s.Store.InstanceIDsByTask(ctx, taskID)
		if err != nil {
			return taskPlan{}, fmt.Errorf("读取引用因子任务 %s 的实例失败，暂不能删除：%w", taskID, err)
		}
		others := make([]string, 0, len(referrers))
		for _, id := range referrers {
			if id != instance.InstanceID {
				others = append(others, id)
			}
		}
		if len(others) > 0 {
			return taskPlan{}, rejection{fmt.Errorf("因子任务 %s 关联在本实例上、会随本实例删除：先删除引用它的实例 %s，再删除本实例", taskID, strings.Join(others, "、"))}
		}
		plan.Delete = append(plan.Delete, taskID)
	}
	return plan, nil
}

// closeBeforeDelete 在动手删除之前，对实例最近一次挂过组合账户的会话再通知一次 Trade 关闭（幂等，账户取会话里记的那个）：会话行随实例
// 删除后，停用时没确认的关闭就再也没有重试入口了。Trade 没确认就拒绝删除；从没挂过账户的实例不联系 Trade。
func (s *Service) closeBeforeDelete(ctx context.Context, instance store.Instance) error {
	session, found, err := s.Store.LatestAccountSession(ctx, instance.InstanceID)
	if err != nil {
		return fmt.Errorf("读取实例最近一次挂账户的会话失败，暂不能删除：%w", err)
	}
	if !found {
		return nil
	}
	if err := s.closeTradeSession(ctx, instance.SpaceID, session); err != nil {
		return fmt.Errorf("Trade 未确认关闭实例在组合账户 %s 上的会话，暂不能删除，请稍后重试：%w", *session.LogicalAccountID, err)
	}
	return nil
}

// checkTasksExist 在启用新建会话之前确认登记的因子任务都还在：任务已不存在（删除实例做到一半，或任务被另外删掉）时直接给出明确的报错，
// 不让解析把“View 不存在”当成“还在创建”一直重试。未接线时不检查。
func (s *Service) checkTasksExist(ctx context.Context, instance store.Instance) error {
	if s.Tasks == nil {
		return nil
	}
	for _, taskID := range input.TaskIDs(instance.FactorTasks) {
		if _, err := s.Tasks.GetFactorTask(ctx, taskID); errors.Is(err, upstream.ErrFactorTaskNotFound) {
			return fmt.Errorf("因子任务 %s：%s", taskID, errTaskMissing)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// missingTasksBudget 是列表与详情里检查因子任务还在不在的时间预算：列表与详情的路由超时只有 5 秒，Factor 慢时宁可不标，
// 也不能拖到超时报错。
const missingTasksBudget = 1500 * time.Millisecond

// markMissingTasks 给停用实例标出引用的、已在 Factor 里不存在的因子任务（missing_factor_tasks）。perTask 为真（详情）时逐个读任务，否则（列表）列一次全部任务；
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
			if errors.Is(err, upstream.ErrFactorTaskNotFound) {
				return false, nil
			}
			return err == nil, err
		}
	} else {
		existing, err := s.Tasks.ListFactorTaskIDs(ctx)
		if err != nil {
			log.Warnf("检查实例的因子任务是否还在失败，本次不标出引用任务已不存在的实例：%v", upstream.RawCause(err))
			return
		}
		exists = func(taskID string) (bool, error) { return existing[taskID], nil }
	}
	missing := make(map[int][]string, len(checked))
	for _, i := range checked {
		for _, taskID := range input.TaskIDs(instances[i].FactorTasks) {
			found, err := exists(taskID)
			if err != nil {
				log.Warnf("检查实例 %s 的因子任务 %s 是否还在失败，本次不标出：%v", instances[i].InstanceID, taskID, upstream.RawCause(err))
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
