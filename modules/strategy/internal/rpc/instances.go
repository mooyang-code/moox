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
	// 创建时就按任务源数据集核对“市场 → 任务”：调参的实例启用时会把任务永久占住，填错了实例启用不了又删不掉。
	if s.Resolver == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errDependenciesUnavailable)}, nil
	}
	if err := input.CheckRegisteredMarkets(ctx, factorTasks, func(ctx context.Context, taskID string) (string, error) {
		return s.Resolver.TaskMarket(ctx, scoped, taskID)
	}); err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if v.GetEnabled() && instance.LogicalAccountID != nil && s.Trade == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("未接线 Trade，不能启用绑定账户的实例"))}, nil
	}
	if err := s.Store.CreateInstance(ctx, instance); err != nil {
		if existing, getErr := s.Store.GetInstance(ctx, instanceID); getErr == nil {
			return createRetryResponse(existing, instance, v.GetEnabled()), nil
		}
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("创建实例失败：%w", err))}, nil
	}
	// 创建已持久化；之后的启用失败也要返回实例身份，并在报错里说明“已创建但未启用”和下一步命令：
	// 调用方（strategy instance create --enable）只看到一个失败，不说明的话会以为没建成、换个参数再 create 一次。
	createdFailure := func(err error) *strategypb.CreateStrategyInstanceRsp {
		current, getErr := s.Store.GetInstance(ctx, instanceID)
		if getErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{
				RetInfo:  invalid(fmt.Errorf("实例 %q 已创建但未启用，而且读不出它的状态；不要再次 create，先 moox-cli strategy instance show 查看，再用 moox-cli strategy instance enable --instance-id %s 启用：%w", instanceID, instanceID, errors.Join(err, fmt.Errorf("读取已创建的实例：%w", getErr)))),
				Instance: &strategypb.StrategyInstance{InstanceId: instanceID, StrategyId: instance.StrategyID, SpaceId: instance.SpaceID, FactorTasks: instance.FactorTasks},
			}
		}
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("实例 %q 已创建但未启用（启用失败）：%w。不要再次 create；修好原因后用 moox-cli strategy instance enable --instance-id %s 启用，不要了就用 strategy instance delete 删除", instanceID, err, instanceID)), Instance: instanceProto(current)}
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
			// 重复启用幂等：不新建会话，也不联系 Trade。
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

// enable 执行启用（批 3 的 3.6）：解析绑定 → 编译 → 核对覆盖 → 绑了组合账户时读一次 Trade 的账户，核对控制模式与市场（只读）→
// 一个事务里写会话（带开始时刻与当时的组合账户）、挂到实例上并置为启用（store.EnableInstance）。任何一步失败都不留下会话。
// 新会话的 DSL 版本、绑定解析与成员和规则状态所属会话的一致时沿用规则状态，否则清空；停用期间改过实例（账户、定义）的，状态在修改时已清空。
func (s *Service) enable(ctx context.Context, instance store.Instance) (store.Instance, error) {
	reload := func() store.Instance {
		if current, err := s.Store.GetInstance(ctx, instance.InstanceID); err == nil {
			return current
		}
		return instance
	}
	if instance.LogicalAccountID != nil {
		if s.Trade == nil {
			return instance, errors.New("未接线 Trade，不能启用绑定账户的实例")
		}
		// 同一空间的组合账户只能由一个启用实例持有（唯一索引兜底）：在解析与联系 Trade 之前检查。
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
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return reload(), err
	}
	if instance.LogicalAccountID != nil {
		if err := s.Trade.CheckAccount(ctx, instance.SpaceID, *instance.LogicalAccountID, resolved.Market.Markets()); err != nil {
			return reload(), err
		}
	}
	sessionID, err := store.NewID()
	if err != nil {
		return reload(), err
	}
	if err := s.Store.EnableInstance(ctx, store.Session{SessionID: sessionID, InstanceID: instance.InstanceID, DSLHash: definition.DSLHash, ResolvedJSON: string(resolvedJSON), MembersJSON: membersJSON, CreatedAt: s.nowTime()}, definition.DSLYaml); err != nil {
		return reload(), err
	}
	if membersJSON != "" && s.Warmer != nil {
		s.Warmer.Start(ctx, instance.InstanceID, sessionID)
	}
	return reload(), nil
}

// checkLiveCalendar 拒绝在股票市场的内嵌日历过了可用截止日后启用实例：此后每一期都推算不出有效期，结果都会被丢弃。
func checkLiveCalendar(resolved input.Resolved, now time.Time) error {
	if err := input.StockCalendarReadiness(resolved.Calendar, now, 0, trigger.ValidBars); err != nil {
		return fmt.Errorf("不能启用实例：%w", err)
	}
	return nil
}

// normalizeFactorTasks 校验并规范化请求里的 factor_tasks（任务键 → 因子任务）：键是市场（spot、swap、equity），附加来源的任务写
// 来源名:市场；主帧与参考市场的任务里股票不能与其他市场混用，任务 ID 必须合法且不重复，个数见
// store.ValidateTaskKeys。
func normalizeFactorTasks(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("factor_tasks 不能为空：按市场（spot、swap、equity）给出因子任务，附加来源写 来源名:市场")
	}
	tasks := make(map[string]string, len(raw))
	seen := map[string]string{}
	for key, taskID := range raw {
		key, taskID = strings.TrimSpace(key), strings.TrimSpace(taskID)
		source, market := dsl.SplitTaskKey(key)
		if market != dsl.MarketSpot && market != dsl.MarketSwap && market != dsl.MarketEquity {
			return nil, fmt.Errorf("factor_tasks 的市场 %q 不受支持，只能是 spot、swap 或 equity；附加来源写 来源名:市场", market)
		}
		if strings.Contains(key, ":") && !dsl.ValidSourceName(source) {
			return nil, fmt.Errorf("factor_tasks 的键 %q 不合法：来源名必须是小写字母开头的 snake_case", key)
		}
		if _, err := input.ViewIDOfTask(taskID); err != nil {
			return nil, fmt.Errorf("factor_tasks 的 %s 任务 ID 不合法：%w", key, err)
		}
		if other, dup := seen[taskID]; dup {
			return nil, fmt.Errorf("因子任务 %s 同时登记在 %s 和 %s", taskID, other, key)
		}
		seen[taskID] = key
		tasks[key] = taskID
	}
	plain := 0
	for key := range tasks {
		if source, _ := dsl.SplitTaskKey(key); source == "" {
			plain++
		}
	}
	if _, equity := tasks[dsl.MarketEquity]; equity && plain > 1 {
		return nil, errors.New("股票（equity）不能与现货、永续混用")
	}
	if err := store.ValidateTaskKeys(tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// disable 执行停用（批 3 的 3.6）：启用的实例在一个事务里停用、关闭会话并清掉实例上的会话（store.DisableInstance），提交之后对
// 刚关闭的会话调一次 Trade 的 CloseTargetSession，组合账户取会话里记的那个（不受之后改账户影响）；会话没挂账户（观察实例）时不调。
// 对已停用的实例再停用一次，按它最近一次挂过账户的会话、会话里记的账户再调一次（幂等），这是 Trade 没确认时唯一的重试入口：
// 不做后台重试。Trade 没确认时返回错误，实例已经停用；Trade 最多把最后一份目标执行到它原本的有效期结束，要立刻停手就在 Trade 暂停组合。
func (s *Service) disable(ctx context.Context, instance store.Instance) (store.Instance, error) {
	reload := func() store.Instance {
		if current, err := s.Store.GetInstance(ctx, instance.InstanceID); err == nil {
			return current
		}
		return instance
	}
	var session store.Session
	found := false
	if instance.Enabled {
		sessionID := ""
		if instance.SessionID != nil {
			sessionID = *instance.SessionID
		}
		if err := s.Store.DisableInstance(ctx, instance.InstanceID, sessionID, s.nowTime()); err != nil {
			return reload(), err
		}
		if s.Warmer != nil {
			s.Warmer.Stop(instance.InstanceID)
		}
		closed, err := s.Store.GetSession(ctx, sessionID)
		if err != nil {
			return reload(), fmt.Errorf("实例已停用，但读取会话失败、没有通知 Trade 关闭会话：%w；再点一次停用重试", err)
		}
		session, found = closed, closed.LogicalAccountID != nil
	} else {
		latest, ok, err := s.Store.LatestAccountSession(ctx, instance.InstanceID)
		if err != nil {
			return reload(), fmt.Errorf("读取实例最近一次挂账户的会话失败：%w", err)
		}
		session, found = latest, ok
	}
	if !found {
		return reload(), nil
	}
	if err := s.closeTradeSession(ctx, instance.SpaceID, session); err != nil {
		return reload(), fmt.Errorf("实例已停用，但 Trade 未确认关闭会话（%w）：再点一次停用重试；要立刻停手，在 Trade 暂停组合账户 %s", err, *session.LogicalAccountID)
	}
	return reload(), nil
}

// closeTradeSession 对挂过组合账户的会话调一次 Trade 的 CloseTargetSession，账户取会话里记的那个。
func (s *Service) closeTradeSession(ctx context.Context, spaceID string, session store.Session) error {
	if session.LogicalAccountID == nil {
		return nil
	}
	if s.Trade == nil {
		return errors.New("未接线 Trade")
	}
	return s.Trade.CloseSession(ctx, spaceID, *session.LogicalAccountID, session.InstanceID, session.SessionID, session.StartedAt)
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
