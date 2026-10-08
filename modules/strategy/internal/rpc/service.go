// Package rpc 实现 StrategyMgr 管理接口：定义、实例、结果、回放与试算。
package rpc

import (
	"context"
	"errors"
	"fmt"
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

// Resolver 解析绑定并装配最新周期（试算）。
type Resolver interface {
	Resolve(ctx context.Context, spaceID, viewID string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error)
	LoadLatest(ctx context.Context, spaceID string, resolved input.Resolved, program *dsl.Program, now time.Time) (input.Loaded, error)
}

// Owner 是 Trade 组合账户的会话所有权接口。
type Owner interface {
	ClaimSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
	ReleaseSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
	ValidateSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error
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

// publicMessage 是返回给接口调用方的错误信息：存储驱动与 Trade 传输的原始错误只写日志。
func publicMessage(err error) string {
	message, replaced := store.FriendlyMessage(err)
	var transport *tradeowner.TransportError
	if replaced || errors.As(err, &transport) {
		log.Warnf("策略接口返回错误：%s；原始错误：%v", message, rawCause(err))
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
