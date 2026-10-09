// Package rpc 实现 StrategyMgr 管理接口：定义、实例、结果、回放与试算。
package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Resolver 解析绑定、装配最新周期（试算）并校验回放区间。
type Resolver interface {
	Resolve(ctx context.Context, spaceID, viewID string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error)
	// CheckAgeCoverage 在启用时校验 View 能追溯 min_age_bars。
	CheckAgeCoverage(ctx context.Context, spaceID string, resolved input.Resolved) error
	LoadLatest(ctx context.Context, spaceID string, resolved input.Resolved, program *dsl.Program, now time.Time) (input.Loaded, error)
	// ReplayWindow 校验并截断回放区间，返回截断后的终点与校验所用的活动索引 ID。
	ReplayWindow(ctx context.Context, spaceID string, resolved input.Resolved, program *dsl.Program, start, end time.Time) (time.Time, string, error)
}

// Owner 是 Trade 组合账户的会话所有权接口。
type Owner interface {
	ClaimSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
	ReleaseSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
	ValidateSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
}

// Alerter 接收需要人工处理的实例事件（例如因 Trade 拒绝会话被自动停用），用于模块健康告警。
type Alerter interface {
	Alert(instance store.Instance, reason string)
}

// ReplayWaker 通知回放执行循环有新任务。
type ReplayWaker interface {
	Wake()
}

// Service 实现 StrategyMgr。
type Service struct {
	Store    *store.Store
	Resolver Resolver
	Owner    Owner
	Replays  ReplayWaker
	Alerts   Alerter
	Now      func() time.Time

	strategyLocks sync.Map
}

func (s *Service) nowTime() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// lockStrategy 串行化同一定义下的修改与实例启停。
func (s *Service) lockStrategy(strategyID string) func() {
	value, _ := s.strategyLocks.LoadOrStore(strategyID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// lockStrategies 按 ID 排序依次加锁（去重），避免两个请求以相反顺序加锁而死锁。
func (s *Service) lockStrategies(strategyIDs ...string) func() {
	ids := append([]string(nil), strategyIDs...)
	sort.Strings(ids)
	unlocks := make([]func(), 0, len(ids))
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			continue
		}
		unlocks = append(unlocks, s.lockStrategy(id))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

// lockInstance 锁住实例当前引用的定义（以及 extra 中的定义，例如改绑的目标），返回拿锁后重读的实例；
// 等锁期间实例被改绑到别的定义时换锁重试。所有修改实例或其会话的路径都经过这里。
func (s *Service) lockInstance(ctx context.Context, instanceID string, extra ...string) (store.Instance, func(), error) {
	instance, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return store.Instance{}, nil, err
	}
	for attempt := 0; ; attempt++ {
		unlock := s.lockStrategies(append([]string{instance.StrategyID}, extra...)...)
		current, err := s.Store.GetInstance(ctx, instanceID)
		if err != nil {
			unlock()
			return store.Instance{}, nil, err
		}
		if current.StrategyID == instance.StrategyID {
			return current, unlock, nil
		}
		unlock()
		if attempt >= 3 {
			return store.Instance{}, nil, errors.New("实例正在被并发修改，请稍后重试")
		}
		instance = current
	}
}

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: publicMessage(err)}
}

func failure(err error) *commonpb.RetInfo {
	if errors.Is(err, store.ErrNotFound) {
		return &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: publicMessage(err)}
	}
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: publicMessage(err)}
}

// publicMessage 是返回给接口调用方的错误信息：存储驱动、Storage/Factor 与 Trade 传输的原始错误只写日志。
func publicMessage(err error) string {
	message, _ := store.FriendlyMessage(err)
	if raw := rawCause(err); raw != nil && raw.Error() != message && !strings.Contains(message, raw.Error()) {
		log.Warnf("策略接口返回错误：%s；原始错误：%v", message, raw)
	}
	if detail := tradeowner.Detail(err); detail != "" {
		log.Warnf("策略接口返回错误：%s；Trade 的说明：%s", message, detail)
	}
	return message
}

// rawCause 返回错误链最内层的原始错误，用于日志。
func rawCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func requestSpaceID(ctx context.Context) string {
	for _, key := range []string{"space_id", "X-Space-Id", "x-space-id"} {
		if value := string(trpc.GetMetaData(ctx, key)); value != "" {
			return value
		}
	}
	return ""
}

func requireSpaceID(ctx context.Context) (string, error) {
	if value := requestSpaceID(ctx); value != "" {
		return value, nil
	}
	return "", errors.New("缺少 space_id 元数据")
}

func (s *Service) ready() error {
	if s == nil || s.Store == nil {
		return errors.New("策略存储不可用")
	}
	return nil
}

// parseDefinition 解析并校验 DSL 文本，返回策略与内容哈希。
func parseDefinition(raw string) (dsl.Strategy, string, error) {
	if strings.TrimSpace(raw) == "" {
		return dsl.Strategy{}, "", errors.New("dsl_yaml 不能为空")
	}
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		return dsl.Strategy{}, "", err
	}
	return strategy, dsl.Hash([]byte(raw)), nil
}

func (s *Service) CreateStrategy(ctx context.Context, req *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || req.GetStrategy() == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(errors.New("strategy 不能为空"))}, nil
	}
	strategy, hash, err := parseDefinition(req.GetStrategy().GetDslYaml())
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	id := strings.TrimSpace(req.GetStrategy().GetStrategyId())
	if id == "" {
		if id, err = store.NewID(); err != nil {
			return &strategypb.CreateStrategyRsp{RetInfo: failure(err)}, nil
		}
	}
	now := s.nowTime()
	def := store.Definition{StrategyID: id, Name: strategy.Name, DSLYaml: req.GetStrategy().GetDslYaml(), DSLHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.CreateDefinition(ctx, def); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(fmt.Errorf("创建策略定义失败：%w", err))}, nil
	}
	return &strategypb.CreateStrategyRsp{RetInfo: success(), Strategy: definitionProto(def)}, nil
}

func (s *Service) UpdateStrategy(ctx context.Context, req *strategypb.UpdateStrategyReq) (*strategypb.UpdateStrategyRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetStrategyId()) == "" {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(errors.New("strategy_id 不能为空"))}, nil
	}
	strategy, hash, err := parseDefinition(req.GetDslYaml())
	if err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	unlock := s.lockStrategy(req.GetStrategyId())
	defer unlock()
	def := store.Definition{StrategyID: req.GetStrategyId(), Name: strategy.Name, DSLYaml: req.GetDslYaml(), DSLHash: hash, UpdatedAt: s.nowTime()}
	if err := s.Store.UpdateDefinition(ctx, def); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &strategypb.UpdateStrategyRsp{RetInfo: failure(err)}, nil
		}
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	updated, err := s.Store.GetDefinition(ctx, req.GetStrategyId())
	if err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.UpdateStrategyRsp{RetInfo: success(), Strategy: definitionProto(updated)}, nil
}

func (s *Service) GetStrategy(ctx context.Context, req *strategypb.GetStrategyReq) (*strategypb.GetStrategyRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetStrategyId()) == "" {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(errors.New("strategy_id 不能为空"))}, nil
	}
	def, err := s.Store.GetDefinition(ctx, req.GetStrategyId())
	if err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.GetStrategyRsp{RetInfo: success(), Strategy: definitionProto(def)}, nil
}

func (s *Service) ListStrategies(ctx context.Context, req *strategypb.ListStrategiesReq) (*strategypb.ListStrategiesRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: invalid(err)}, nil
	}
	defs, err := s.Store.ListDefinitions(ctx)
	if err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: failure(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(defs))
	items := make([]*strategypb.Strategy, 0, end-start)
	for _, def := range defs[start:end] {
		items = append(items, definitionProto(def))
	}
	return &strategypb.ListStrategiesRsp{RetInfo: success(), Strategies: items, Total: int64(len(defs)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) DeleteStrategy(ctx context.Context, req *strategypb.DeleteStrategyReq) (*strategypb.DeleteStrategyRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.DeleteStrategyRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.DeleteStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetStrategyId()) == "" {
		return &strategypb.DeleteStrategyRsp{RetInfo: invalid(errors.New("strategy_id 不能为空"))}, nil
	}
	unlock := s.lockStrategy(req.GetStrategyId())
	defer unlock()
	if err := s.Store.SoftDeleteDefinition(ctx, req.GetStrategyId(), s.nowTime()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &strategypb.DeleteStrategyRsp{RetInfo: failure(err)}, nil
		}
		return &strategypb.DeleteStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.DeleteStrategyRsp{RetInfo: success()}, nil
}
