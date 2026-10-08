package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
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
	if strings.TrimSpace(v.GetStrategyId()) == "" || strings.TrimSpace(v.GetViewId()) == "" {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("strategy_id 与 view_id 不能为空"))}, nil
	}
	instanceID := strings.TrimSpace(v.GetInstanceId())
	if instanceID == "" {
		if instanceID, err = store.NewID(); err != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(err)}, nil
		}
	}
	unlock := s.lockStrategy(v.GetStrategyId())
	defer unlock()
	now := s.nowTime()
	instance := store.Instance{InstanceID: instanceID, StrategyID: v.GetStrategyId(), SpaceID: scoped, ViewID: strings.TrimSpace(v.GetViewId()), LogicalAccountID: optionalString(v.GetLogicalAccountId()), CreatedAt: now, UpdatedAt: now}
	if existing, err := s.Store.GetInstance(ctx, instanceID); err == nil {
		return createRetryResponse(existing, instance, v.GetEnabled()), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	if def, err := s.Store.GetDefinition(ctx, instance.StrategyID); err != nil || def.DeletedAt != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("策略定义 %s 不存在", instance.StrategyID))}, nil
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
				Instance: &strategypb.StrategyInstance{InstanceId: instanceID, StrategyId: instance.StrategyID, SpaceId: instance.SpaceID, ViewId: instance.ViewID},
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
	if existing.DeletedAt != nil || existing.SpaceID != requested.SpaceID || existing.StrategyID != requested.StrategyID || existing.ViewID != requested.ViewID || !sameAccount {
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
	current, err := s.Store.GetInstance(ctx, v.GetInstanceId())
	if err != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	if current.SpaceID != scoped || current.DeletedAt != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	unlock := s.lockStrategy(current.StrategyID)
	defer unlock()
	updated := current
	if strings.TrimSpace(v.GetStrategyId()) != "" {
		updated.StrategyID = strings.TrimSpace(v.GetStrategyId())
	}
	if strings.TrimSpace(v.GetViewId()) != "" {
		updated.ViewID = strings.TrimSpace(v.GetViewId())
	}
	updated.LogicalAccountID = optionalString(v.GetLogicalAccountId())
	updated.UpdatedAt = s.nowTime()
	if def, err := s.Store.GetDefinition(ctx, updated.StrategyID); err != nil || def.DeletedAt != nil {
		return &strategypb.UpdateStrategyInstanceRsp{RetInfo: invalid(fmt.Errorf("策略定义 %s 不存在", updated.StrategyID))}, nil
	}
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
	return &strategypb.GetStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(instance)}, nil
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
	return &strategypb.ListStrategyInstancesRsp{RetInfo: success(), Instances: items, Total: int64(len(values)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) DeleteStrategyInstance(ctx context.Context, req *strategypb.DeleteStrategyInstanceReq) (*strategypb.DeleteStrategyInstanceRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id 不能为空"))}, nil
	}
	instance, err := s.Store.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(err)}, nil
	}
	if instance.SpaceID != scoped {
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	unlock := s.lockStrategy(instance.StrategyID)
	defer unlock()
	if err := s.Store.SoftDeleteInstance(ctx, instance.InstanceID, s.nowTime()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &strategypb.DeleteStrategyInstanceRsp{RetInfo: failure(err)}, nil
		}
		return &strategypb.DeleteStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.DeleteStrategyInstanceRsp{RetInfo: success()}, nil
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
	instance, err := s.Store.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: failure(err)}, nil
	}
	if instance.SpaceID != scoped {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("实例不在当前空间"))}, nil
	}
	unlock := s.lockStrategy(instance.StrategyID)
	defer unlock()
	// 第一次读取只用来选锁；拿锁后重读，避免并发启停作用在过期快照上。
	instance, err = s.Store.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: failure(err)}, nil
	}
	if instance.DeletedAt != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("实例已删除"))}, nil
	}
	if req.GetEnabled() {
		if instance.Enabled {
			// 重复启用幂等：不新建会话；绑定账户时核对 Trade 的所有者仍是本会话。
			if instance.LogicalAccountID != nil && instance.SessionID != nil && s.Owner != nil {
				if err := s.Owner.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
					return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(fmt.Errorf("实例的会话已不再被 Trade 授权：%w", err))}, nil
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
	definition, err := s.Store.GetDefinition(ctx, instance.StrategyID)
	if err != nil || definition.DeletedAt != nil {
		return instance, fmt.Errorf("策略定义 %s 不存在", instance.StrategyID)
	}
	sessionID := ""
	var resolvedJSON json.RawMessage
	if instance.SessionID != nil && strings.TrimSpace(*instance.SessionID) != "" {
		previous := *instance.SessionID
		if session, err := s.Store.GetSession(ctx, previous); err == nil && session.ClosedAt == nil && session.DSLHash == definition.DSLHash {
			sessionID = session.SessionID
			resolvedJSON = json.RawMessage(session.ResolvedJSON)
		} else {
			if instance.LogicalAccountID != nil {
				if err := s.Owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, previous); err != nil && !tradeowner.IsOwnerConflict(err) {
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
		strategy, err := dsl.Parse([]byte(definition.DSLYaml))
		if err != nil {
			return reload(), err
		}
		if s.Resolver == nil {
			return reload(), errors.New("Storage 与 Factor 依赖未配置，不能启用实例")
		}
		resolved, _, err := s.Resolver.Resolve(ctx, instance.SpaceID, instance.ViewID, strategy)
		if err != nil {
			return reload(), err
		}
		resolvedJSON, err = json.Marshal(resolved)
		if err != nil {
			return reload(), err
		}
		if sessionID, err = store.NewID(); err != nil {
			return reload(), err
		}
		now := s.nowTime()
		if err := s.Store.OpenSession(ctx, store.Session{SessionID: sessionID, InstanceID: instance.InstanceID, DSLHash: definition.DSLHash, ResolvedJSON: string(resolvedJSON), CreatedAt: now}, definition.DSLYaml); err != nil {
			return reload(), fmt.Errorf("写入会话快照失败：%w", err)
		}
		// 先把会话写到停用状态的实例上再联系 Trade：认领响应丢失时，启动对账能释放这个确定的身份。
		if err := s.Store.SetInstanceEnabled(ctx, instance.InstanceID, false, &sessionID, resolvedJSON, now); err != nil {
			return reload(), err
		}
	}
	if instance.LogicalAccountID != nil {
		if err := s.Owner.ClaimSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, sessionID); err != nil {
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
	return reload(), nil
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
	if err := s.Store.SetInstanceEnabled(ctx, instance.InstanceID, false, pending, nil, s.nowTime()); err != nil {
		return reload(), err
	}
	if instance.SessionID != nil {
		_ = s.Store.CloseSession(ctx, *instance.SessionID, s.nowTime())
	}
	if releaseNeeded {
		if s.Owner == nil {
			return reload(), errors.New("未接线 Trade，无法释放账户会话；请接线后重试停用")
		}
		if err := s.Owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil && !tradeowner.IsOwnerConflict(err) {
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
		unlock := s.lockStrategy(instance.StrategyID)
		func() {
			defer unlock()
			current, err := s.Store.GetInstance(ctx, instance.InstanceID)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：重读失败：%w", instance.InstanceID, err))
				return
			}
			// 拿锁后重读：正在进行的启用握手不能被当成过期的停用会话释放掉。
			if current.Enabled || current.SessionID == nil {
				return
			}
			if current.LogicalAccountID != nil {
				if s.Owner == nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("实例 %s：未接线 Trade，无法释放会话", current.InstanceID))
					return
				}
				if err := s.Owner.ReleaseSession(ctx, current.SpaceID, *current.LogicalAccountID, current.InstanceID, *current.SessionID); err != nil && !tradeowner.IsOwnerConflict(err) {
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

// ReconcileEnabledInstances 在开始消费事件前核对启用实例仍持有 Trade 会话。不一致只影响对应实例，
// 返回汇总错误供告警，不阻塞进程启动（接口必须可用，才能人工修复）：
// Trade 明确不再授权该会话（所有者已变或账户不存在）时实例无法继续交易，自动停用；
// 结果未知（Trade 不可达等）时保持启用并标记 degraded，目标事件由 Trade 按会话围栏校验。
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
	for _, instance := range instances {
		if instance.LogicalAccountID == nil || instance.SessionID == nil || strings.TrimSpace(*instance.SessionID) == "" {
			continue
		}
		err := s.Owner.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID)
		if err == nil {
			continue
		}
		if tradeowner.IsSessionRejected(err) {
			if disableErr := s.Store.SetInstanceEnabled(ctx, instance.InstanceID, false, nil, nil, s.nowTime()); disableErr != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("启用实例 %s 的会话已被 Trade 拒绝，自动停用失败：%w", instance.InstanceID, disableErr))
				continue
			}
			_ = s.Store.CloseSession(ctx, *instance.SessionID, s.nowTime())
			_ = s.Store.SetInstanceHealth(ctx, instance.InstanceID, store.HealthDegraded, s.nowTime())
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("启用实例 %s 的会话已被 Trade 拒绝，已自动停用：%w", instance.InstanceID, err))
			continue
		}
		_ = s.Store.SetInstanceHealth(ctx, instance.InstanceID, store.HealthDegraded, s.nowTime())
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("启用实例 %s 的 Trade 会话暂时无法校验：%w", instance.InstanceID, err))
	}
	return reconcileErr
}
