package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *Service) CreateStrategyInstance(ctx context.Context, req *strategypb.CreateStrategyInstanceReq) (*strategypb.CreateStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || req.GetInstance() == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance 不能为空"))}, nil
	}
	v := req.GetInstance()
	if v.GetSpaceId() != "" && v.GetSpaceId() != scoped {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	if strings.TrimSpace(v.GetStrategyId()) == "" {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("strategy_id 不能为空"))}, nil
	}
	factorTasks, err := normalizeFactorTasks(v.GetFactorTasks())
	if err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	instanceID := strings.TrimSpace(v.GetInstanceId())
	if instanceID == "" {
		if instanceID, err = store.NewID(); err != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(err)}, nil
		}
	}
	unlock, lockErr := s.lockStrategy(ctx, v.GetStrategyId())
	if lockErr != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(lockErr)}, nil
	}
	defer unlock()
	now := s.nowTime()
	instance := store.Instance{InstanceID: instanceID, StrategyID: v.GetStrategyId(), SpaceID: scoped, FactorTasks: factorTasks, LogicalAccountID: optionalString(v.GetLogicalAccountId()), CreatedAt: now, UpdatedAt: now}
	if existing, err := s.Store.GetInstance(ctx, instanceID); err == nil {
		return createRetryResponse(existing, instance, v.GetEnabled()), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	if def, err := s.Store.GetDefinition(ctx, instance.StrategyID); err != nil || def.DeletedAt != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("策略定义 %s 不存在", instance.StrategyID))}, nil
	}
	// 创建时就按任务源数据集核对“市场 → 任务”：任务一旦关联实例就永久占住，填错了实例启用不了又删不掉。
	if s.Resolver == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errDependenciesUnavailable)}, nil
	}
	if err := input.CheckRegisteredMarkets(ctx, factorTasks, func(ctx context.Context, taskID string) (string, error) {
		return s.Resolver.TaskMarket(ctx, scoped, taskID)
	}); err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if v.GetEnabled() && instance.LogicalAccountID != nil && s.Owner == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("未接线 Trade，不能启用绑定账户的实例"))}, nil
	}
	if err := s.Store.CreateInstance(ctx, instance); err != nil {
		if existing, getErr := s.Store.GetInstance(ctx, instanceID); getErr == nil {
			return createRetryResponse(existing, instance, v.GetEnabled()), nil
		}
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("创建实例失败：%w", err))}, nil
	}
	// 创建已持久化；之后的启用失败也要返回实例身份，便于用 SetStrategyInstanceEnabled 恢复。
	createdFailure := func(err error) *strategypb.CreateStrategyInstanceRsp {
		current, getErr := s.Store.GetInstance(ctx, instanceID)
		if getErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{
				RetInfo:  invalid(fmt.Errorf("实例 %q 已创建但状态不可读，请先查看实例再用 SetStrategyInstanceEnabled 启用：%w", instanceID, errors.Join(err, fmt.Errorf("读取已创建的实例：%w", getErr)))),
				Instance: &strategypb.StrategyInstance{InstanceId: instanceID, StrategyId: instance.StrategyID, SpaceId: instance.SpaceID, FactorTasks: instance.FactorTasks},
			}
		}
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("实例 %q 已创建；请查看实例并用 SetStrategyInstanceEnabled 启用：%w", instanceID, err)), Instance: instanceProto(current)}
	}
	if v.GetEnabled() {
		if _, err := s.enable(ctx, instance); err != nil {
			return createdFailure(err), nil
		}
	}
	created, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return createdFailure(err), nil
	}
	return &strategypb.CreateStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(created)}, nil
}

// createRetryResponse 处理重复创建：配置一致时返回现有实例，启用状态不同时提示用显式启用接口。
func createRetryResponse(existing, requested store.Instance, desiredEnabled bool) *strategypb.CreateStrategyInstanceRsp {
	sameAccount := (existing.LogicalAccountID == nil && requested.LogicalAccountID == nil) || (existing.LogicalAccountID != nil && requested.LogicalAccountID != nil && *existing.LogicalAccountID == *requested.LogicalAccountID)
	if existing.SpaceID != requested.SpaceID || existing.StrategyID != requested.StrategyID || !store.SameTasks(existing.FactorTasks, requested.FactorTasks) || !sameAccount {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 与已有实例的配置冲突"))}
	}
	response := &strategypb.CreateStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(existing)}
	if existing.Enabled != desiredEnabled {
		response.RetInfo = invalid(fmt.Errorf("实例 %q 已存在且 enabled=%t；请用 SetStrategyInstanceEnabled 改变启用状态", existing.InstanceID, existing.Enabled))
	}
	return response
}

func (s *Service) UpdateStrategyInstance(ctx context.Context, req *strategypb.UpdateStrategyInstanceReq) (*strategypb.UpdateStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || req.GetInstance() == nil || strings.TrimSpace(req.GetInstance().GetInstanceId()) == "" {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	v := req.GetInstance()
	// 可编辑字段整体替换：策略定义必填，组合账户留空表示观察实例。因子任务在创建时写定，不能修改：
	// 请求里给了 factor_task_id 时必须与现有的一致。
	if strings.TrimSpace(v.GetStrategyId()) == "" {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("strategy_id 不能为空"))}, nil
	}
	// 改绑同时锁住实例当前的定义与新定义：启用新定义的实例、修改或删除新定义都不能与改绑交错；
	// 等锁期间实例被并发改绑时换锁重试，保证持有的锁覆盖重读到的定义。
	current, unlock, err := s.lockInstance(ctx, v.GetInstanceId(), strings.TrimSpace(v.GetStrategyId()))
	if err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	defer unlock()
	if current.SpaceID != scoped {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	if len(v.GetFactorTasks()) > 0 {
		if tasks, err := normalizeFactorTasks(v.GetFactorTasks()); err != nil || !store.SameTasks(tasks, current.FactorTasks) {
			return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例的因子任务在创建时写定，不能修改"))}, nil
		}
	}
	updated := current
	updated.StrategyID = strings.TrimSpace(v.GetStrategyId())
	if current.Enabled {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例必须先停用才能修改"))}, nil
	}
	if current.SessionID != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例的启用或停用操作尚未完成"))}, nil
	}
	if def, err := s.Store.GetDefinition(ctx, updated.StrategyID); err != nil || def.DeletedAt != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("策略定义 %s 不存在", updated.StrategyID))}, nil
	}
	updated.LogicalAccountID = optionalString(v.GetLogicalAccountId())
	updated.UpdatedAt = s.nowTime()
	if err := s.Store.UpdateInstance(ctx, updated); err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	reloaded, err := s.Store.GetInstance(ctx, updated.InstanceID)
	if err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.UpdateStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(reloaded)}, nil
}

func (s *Service) GetStrategyInstance(ctx context.Context, req *strategypb.GetStrategyInstanceReq) (*strategypb.GetStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	instance, err := s.Store.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	if instance.SpaceID != scoped {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	item := instanceProto(instance)
	s.markMissingTasks(ctx, []store.Instance{instance}, []*strategypb.StrategyInstance{item}, true)
	return &strategypb.GetStrategyInstanceRsp{RetInfo: success(), Instance: item}, nil
}

func (s *Service) ListStrategyInstances(ctx context.Context, req *strategypb.ListStrategyInstancesReq) (*strategypb.ListStrategyInstancesRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: invalid(err)}, nil
	}
	var enabled *bool
	if req != nil && req.Enabled != nil {
		enabled = req.Enabled
	}
	values, err := s.Store.ListInstances(ctx, scoped, enabled)
	if err != nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: failure(err)}, nil
	}
	if req != nil && strings.TrimSpace(req.GetStrategyId()) != "" {
		filtered := values[:0]
		for _, value := range values {
			if value.StrategyID == req.GetStrategyId() {
				filtered = append(filtered, value)
			}
		}
		values = filtered
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyInstance, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, instanceProto(value))
	}
	s.markMissingTasks(ctx, values[start:end], items, false)
	return &strategypb.ListStrategyInstancesRsp{RetInfo: success(), Instances: items, Total: int64(len(values)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) SetStrategyInstanceEnabled(ctx context.Context, req *strategypb.SetStrategyInstanceEnabledReq) (*strategypb.SetStrategyInstanceEnabledRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	// 拿锁后重读，避免并发启停作用在过期快照上；等锁期间实例被改绑时换成新定义的锁。
	instance, unlock, err := s.lockInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: failure(err)}, nil
	}
	defer unlock()
	if instance.SpaceID != scoped {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	if req.GetEnabled() {
		if instance.Enabled {
			// 重复启用幂等：不新建会话；绑定账户时核对 Trade 的所有者仍是本会话。
			if instance.LogicalAccountID != nil && instance.SessionID != nil && s.Owner != nil {
				if err := s.Owner.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
					if tradeowner.IsSessionRejected(err) {
						return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(fmt.Errorf("实例的会话已不再被 Trade 授权：%w", err))}, nil
					}
					return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(fmt.Errorf("暂时无法向 Trade 核实实例的会话：%w", err))}, nil
				}
			}
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: success(), Instance: instanceProto(instance)}, nil
		}
		enabled, err := s.enable(ctx, instance)
		if err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err), Instance: instanceProto(enabled)}, nil
		}
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: success(), Instance: instanceProto(enabled)}, nil
	}
	disabled, err := s.disable(ctx, instance)
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err), Instance: instanceProto(disabled)}, nil
	}
	return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: success(), Instance: instanceProto(disabled)}, nil
}

// enable 执行启用流程：解析绑定 → 编译 → 固化快照 → 写会话 → 认领账户 → 置为启用。
// 只有上次未完成的启用留下的待定会话（未关闭且定义未变）会被沿用，以便恢复 Trade 可能已经成功的认领；
// 已关闭的会话（停用后释放未确认）必须先在 Trade 侧确认释放，再新建会话，规则状态与绑定快照随之重建。
func (s *Service) enable(ctx context.Context, instance store.Instance) (store.Instance, error) {
	reload := func() store.Instance {
		if current, err := s.Store.GetInstance(ctx, instance.InstanceID); err == nil {
			return current
		}
		return instance
	}
	if instance.LogicalAccountID != nil && s.Owner == nil {
		return instance, errors.New("未接线 Trade，不能启用绑定账户的实例")
	}
	if instance.LogicalAccountID != nil {
		// 同一空间的组合账户只能由一个启用实例持有：在写会话、联系 Trade 之前检查，被拒绝时不留下待定会话。
		if holder, err := s.accountHolder(ctx, instance); err != nil {
			return instance, err
		} else if holder != "" {
			return instance, fmt.Errorf("组合账户 %s 已被启用实例 %s 绑定，请先停用该实例", *instance.LogicalAccountID, holder)
		}
	}
	definition, err := s.Store.GetDefinition(ctx, instance.StrategyID)
	if err != nil || definition.DeletedAt != nil {
		return instance, fmt.Errorf("策略定义 %s 不存在", instance.StrategyID)
	}
	sessionID := ""
	// poolSession 为真表示这次启用用的会话是策略池会话（带成员快照）：启用成功后要开始预热。直接用这里已有的成员快照判断，
	// 不再重读会话——重读失败会让预热没有启动，门禁又看不到任务，事件就被一直重投。
	poolSession := false
	var resolvedJSON json.RawMessage
	if instance.SessionID != nil && strings.TrimSpace(*instance.SessionID) != "" {
		previous := *instance.SessionID
		if session, err := s.Store.GetSession(ctx, previous); err == nil && session.ClosedAt == nil && session.DSLHash == definition.DSLHash && store.SameTasks(sessionTasks(session), instance.FactorTasks) {
			resolved, err := input.ParseResolved([]byte(session.ResolvedJSON))
			if err != nil {
				return reload(), err
			}
			if err := checkLiveCalendar(resolved, s.nowTime()); err != nil {
				return reload(), err
			}
			sessionID = session.SessionID
			resolvedJSON = json.RawMessage(session.ResolvedJSON)
			poolSession = session.MembersJSON != ""
		} else {
			if instance.LogicalAccountID != nil {
				if err := s.Owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, previous); err != nil && !tradeowner.IsReleaseSettled(err) {
					return reload(), fmt.Errorf("上一个会话在 Trade 侧的释放尚未确认，暂不能启用：%w", err)
				}
			}
			_ = s.Store.CloseSession(ctx, previous, s.nowTime())
			if err := s.Store.ClearInstanceSession(ctx, instance.InstanceID, previous, s.nowTime()); err != nil {
				return reload(), fmt.Errorf("清理旧会话失败：%w", err)
			}
		}
	}
	if sessionID == "" {
		if err := s.checkTasksExist(ctx, instance); err != nil {
			return reload(), err
		}
		strategy, err := dsl.Parse([]byte(definition.DSLYaml))
		if err != nil {
			return reload(), err
		}
		if s.Resolver == nil {
			return reload(), errors.New("Storage 与 Factor 依赖未配置，不能启用实例")
		}
		var resolved input.Resolved
		membersJSON := ""
		if strategy.IsPool() {
			members, err := s.checkPool(ctx, strategy)
			if err != nil {
				return reload(), err
			}
			pooled, err := s.Resolver.ResolvePoolInstance(ctx, instance.InstanceID, instance.SpaceID, instance.FactorTasks, strategy, members)
			if err != nil {
				return reload(), err
			}
			if err := checkLiveCalendar(pooled.Resolved, s.nowTime()); err != nil {
				return reload(), err
			}
			if err := s.Resolver.CheckPoolCoverage(ctx, instance.SpaceID, pooled, s.nowTime()); err != nil {
				return reload(), err
			}
			if membersJSON, err = pooled.MembersJSON(); err != nil {
				return reload(), err
			}
			resolved = pooled.Resolved
		} else {
			var err error
			if resolved, _, err = s.Resolver.ResolveInstance(ctx, instance.InstanceID, instance.SpaceID, instance.FactorTasks, strategy); err != nil {
				return reload(), err
			}
			if err := checkLiveCalendar(resolved, s.nowTime()); err != nil {
				return reload(), err
			}
			if err := s.Resolver.CheckCoverage(ctx, instance.SpaceID, resolved, s.nowTime()); err != nil {
				return reload(), err
			}
		}
		resolvedJSON, err = json.Marshal(resolved)
		if err != nil {
			return reload(), err
		}
		poolSession = membersJSON != ""
		if sessionID, err = store.NewID(); err != nil {
			return reload(), err
		}
		now := s.nowTime()
		if err := s.Store.OpenSession(ctx, store.Session{SessionID: sessionID, InstanceID: instance.InstanceID, DSLHash: definition.DSLHash, ResolvedJSON: string(resolvedJSON), MembersJSON: membersJSON, CreatedAt: now}, definition.DSLYaml); err != nil {
			return reload(), fmt.Errorf("写入会话快照失败：%w", err)
		}
		// 先把会话写到停用状态的实例上再联系 Trade：认领响应丢失时，启动对账能释放这个确定的身份。
		// 挂不上（期间实例被改绑或定义被修改）时这个会话从未交给 Trade，直接关闭。
		if err := s.Store.SetInstanceEnabled(ctx, instance.InstanceID, false, &sessionID, resolvedJSON, now); err != nil {
			_ = s.Store.CloseSession(ctx, sessionID, s.nowTime())
			return reload(), err
		}
	}
	if instance.LogicalAccountID != nil {
		resolved, err := input.ParseResolved(resolvedJSON)
		if err != nil {
			return reload(), err
		}
		if err := s.Owner.ClaimSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, sessionID, resolved.Market.Markets()); err != nil {
			if tradeowner.IsPermanentClaimError(err) {
				_ = s.Store.CloseSession(ctx, sessionID, s.nowTime())
				if clearErr := s.Store.ClearInstanceSession(ctx, instance.InstanceID, sessionID, s.nowTime()); clearErr != nil {
					err = errors.Join(err, fmt.Errorf("清除被拒绝的会话失败：%w", clearErr))
				}
			}
			return reload(), err
		}
	}
	if err := s.Store.SetInstanceEnabled(ctx, instance.InstanceID, true, &sessionID, resolvedJSON, s.nowTime()); err != nil {
		if instance.LogicalAccountID != nil {
			_ = s.Owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, sessionID)
		}
		return reload(), err
	}
	if poolSession && s.Warmer != nil {
		s.Warmer.Start(ctx, instance.InstanceID, sessionID)
	}
	return reload(), nil
}

// checkLiveCalendar 拒绝在 A 股内嵌日历过了可用截止日后启用实例：此后每一期都推算不出有效期，结果都会被丢弃。
func checkLiveCalendar(resolved input.Resolved, now time.Time) error {
	if !strings.EqualFold(resolved.Calendar, "cn_stock") {
		return nil
	}
	if err := input.StockCalendarReadiness(now, 0, trigger.ValidBars); err != nil {
		return fmt.Errorf("不能启用实例：%w", err)
	}
	return nil
}

// sessionTasks 返回会话固化的解析结果里的因子任务（市场 → 任务，与存储层启用时的核对口径一致）；解析失败时返回空表。
func sessionTasks(session store.Session) map[string]string {
	resolved, err := input.ParseResolved([]byte(session.ResolvedJSON))
	if err != nil {
		return map[string]string{}
	}
	tasks := make(map[string]string, len(resolved.Tasks))
	for market, task := range resolved.Tasks {
		tasks[market] = task.FactorTaskID
	}
	return tasks
}

// normalizeFactorTasks 校验并规范化请求里的 factor_tasks（市场 → 因子任务）：一个或两个，市场只能是 spot、swap、equity，
// 统一数据集（同时放现货与永续）上的任务登记为 multi；股票、multi 都不能与其他市场混用，任务 ID 必须合法且不重复。
func normalizeFactorTasks(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("factor_tasks 不能为空：按市场（spot、swap、equity）给出因子任务，统一数据集上的任务写 multi")
	}
	tasks := make(map[string]string, len(raw))
	seen := map[string]string{}
	for market, taskID := range raw {
		market, taskID = strings.TrimSpace(market), strings.TrimSpace(taskID)
		if market != dsl.MarketSpot && market != dsl.MarketSwap && market != dsl.MarketEquity && market != input.MarketMulti {
			return nil, fmt.Errorf("factor_tasks 的市场 %q 不受支持，只能是 spot、swap、equity 或 multi（统一数据集上的任务）", market)
		}
		if _, err := input.ViewIDOfTask(taskID); err != nil {
			return nil, fmt.Errorf("factor_tasks 的 %s 任务 ID 不合法：%w", market, err)
		}
		if other, dup := seen[taskID]; dup {
			return nil, fmt.Errorf("因子任务 %s 同时登记在 %s 和 %s 市场", taskID, other, market)
		}
		seen[taskID] = market
		tasks[market] = taskID
	}
	if _, equity := tasks[dsl.MarketEquity]; equity && len(tasks) > 1 {
		return nil, errors.New("股票（equity）不能与现货、永续混用")
	}
	if _, unified := tasks[input.MarketMulti]; unified && len(tasks) > 1 {
		return nil, errors.New("统一数据集上的任务（multi）已同时覆盖现货与永续，不能再绑定其他任务")
	}
	return tasks, nil
}

// disable 执行停用流程：先落库再释放 Trade；释放确认前保留会话，供重启对账恢复。
func (s *Service) disable(ctx context.Context, instance store.Instance) (store.Instance, error) {
	reload := func() store.Instance {
		if current, err := s.Store.GetInstance(ctx, instance.InstanceID); err == nil {
			return current
		}
		return instance
	}
	releaseNeeded := instance.SessionID != nil && instance.LogicalAccountID != nil
	var pending *string
	if releaseNeeded {
		pending = instance.SessionID
	}
	if err := s.Store.DisableInstance(ctx, instance.InstanceID, instance.SessionID, pending, "", s.nowTime()); err != nil {
		return reload(), err
	}
	if s.Warmer != nil {
		s.Warmer.Stop(instance.InstanceID)
	}
	if releaseNeeded {
		if s.Owner == nil {
			return reload(), errors.New("未接线 Trade，无法释放账户会话；请接线后重试停用")
		}
		if err := s.Owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil && !tradeowner.IsReleaseSettled(err) {
			return reload(), fmt.Errorf("已停用但 Trade 释放未确认，稍后自动重试：%w", err)
		}
		if err := s.Store.ClearInstanceSession(ctx, instance.InstanceID, *instance.SessionID, s.nowTime()); err != nil {
			return reload(), err
		}
	}
	return reload(), nil
}

// ReconcileDisabledInstances 完成崩溃或 Trade 响应丢失时留下的停用握手：释放并清空会话。
func (s *Service) ReconcileDisabledInstances(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return nil
	}
	disabled := false
	instances, err := s.Store.ListInstances(ctx, "", &disabled)
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, instance := range instances {
		if instance.SessionID == nil {
			continue
		}
		func() {
			current, unlock, err := s.lockInstance(ctx, instance.InstanceID)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：重读失败：%w", instance.InstanceID, err))
				return
			}
			defer unlock()
			// 拿锁后重读并与列表快照比较：快照之后用户已重新停用、改绑或开始启用时，会话已不是快照里那个，
			// 不能把正在进行的启用握手当成过期的停用会话释放掉。
			if current.Enabled || current.SessionID == nil || *current.SessionID != *instance.SessionID {
				return
			}
			if current.LogicalAccountID != nil {
				if s.Owner == nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：未接线 Trade，无法释放会话", current.InstanceID))
					return
				}
				if err := s.Owner.ReleaseSession(ctx, current.SpaceID, *current.LogicalAccountID, current.InstanceID, *current.SessionID); err != nil && !tradeowner.IsReleaseSettled(err) {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：释放会话失败：%w", current.InstanceID, err))
					return
				}
			}
			if err := s.Store.ClearInstanceSession(ctx, current.InstanceID, *current.SessionID, s.nowTime()); err != nil && !errors.Is(err, store.ErrNotFound) {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：清空会话失败：%w", current.InstanceID, err))
				return
			}
			_ = s.Store.CloseSession(ctx, *current.SessionID, s.nowTime())
		}()
	}
	return reconcileErr
}

// ReconcileEnabledInstances 核对每个绑定账户的启用实例仍持有 Trade 会话：启动时执行一次，之后随对账循环周期执行。
// 不一致只影响对应实例，汇总错误供告警，不阻塞进程（接口必须可用，才能人工修复）：
// Trade 明确不再授权该会话（所有者已变或账户不存在）时实例无法继续交易，自动停用、标记 degraded 并上报告警；
// 结果未知（Trade 不可达等）时保持启用并标记 session_unverified，直到再次核实成功；目标事件由 Trade 按会话围栏校验。
func (s *Service) ReconcileEnabledInstances(ctx context.Context) error {
	if err := s.ready(); err != nil || s.Owner == nil {
		return nil
	}
	enabled := true
	instances, err := s.Store.ListInstances(ctx, "", &enabled)
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, listed := range instances {
		if listed.LogicalAccountID == nil || listed.SessionID == nil || strings.TrimSpace(*listed.SessionID) == "" {
			continue
		}
		if err := s.reconcileEnabled(ctx, listed); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func (s *Service) reconcileEnabled(ctx context.Context, listed store.Instance) error {
	// 拿锁后重读：并发的停用或重新启用已经改变了会话时，以最新状态为准。
	instance, unlock, err := s.lockInstance(ctx, listed.InstanceID)
	if err != nil {
		return fmt.Errorf("实例 %s：重读失败：%w", listed.InstanceID, err)
	}
	defer unlock()
	if !instance.Enabled || instance.LogicalAccountID == nil || instance.SessionID == nil || *instance.SessionID != *listed.SessionID {
		return nil
	}
	err = s.Owner.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID)
	switch {
	case err == nil:
		if instance.Health == store.HealthSessionUnverified {
			_ = s.Store.SetInstanceHealth(ctx, instance.InstanceID, store.HealthOK, s.nowTime())
		}
		return nil
	case tradeowner.IsSessionRejected(err):
		// 停用与标记 degraded 在同一事务：自动停用的原因留在实例上。
		if disableErr := s.Store.DisableInstance(ctx, instance.InstanceID, instance.SessionID, nil, store.HealthDegraded, s.nowTime()); disableErr != nil {
			return fmt.Errorf("启用实例 %s 的会话已被 Trade 拒绝，自动停用失败：%w", instance.InstanceID, disableErr)
		}
		if s.Alerts != nil {
			s.Alerts.Alert(instance, "Trade 不再授权该实例的会话，已自动停用")
		}
		return fmt.Errorf("启用实例 %s 的会话已被 Trade 拒绝，已自动停用：%w", instance.InstanceID, err)
	default:
		if instance.Health != store.HealthSessionUnverified {
			_ = s.Store.SetInstanceHealth(ctx, instance.InstanceID, store.HealthSessionUnverified, s.nowTime())
		}
		return fmt.Errorf("启用实例 %s 的 Trade 会话暂时无法核实：%w", instance.InstanceID, err)
	}
}

// accountHolder 返回同一空间里已绑定该组合账户的其他启用实例；没有时返回空串。
func (s *Service) accountHolder(ctx context.Context, instance store.Instance) (string, error) {
	enabled := true
	instances, err := s.Store.ListInstances(ctx, instance.SpaceID, &enabled)
	if err != nil {
		return "", err
	}
	for _, other := range instances {
		if other.InstanceID != instance.InstanceID && other.LogicalAccountID != nil && *other.LogicalAccountID == *instance.LogicalAccountID {
			return other.InstanceID, nil
		}
	}
	return "", nil
}
