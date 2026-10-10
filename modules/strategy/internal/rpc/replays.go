package rpc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func parseTime(raw, field string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s 不能为空", field)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			// 回放区间按毫秒落库：先截到毫秒，返回的根数与首末根才与执行时一致。
			return at.UTC().Truncate(time.Millisecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("%s 的格式无法识别：%s", field, raw)
}

func (s *Service) StartReplay(ctx context.Context, req *strategypb.StartReplayReq) (*strategypb.StartReplayRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.StartReplayRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetViewId()) == "" {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("view_id 不能为空"))}, nil
	}
	source, err := s.replaySource(ctx, scoped, req)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	strategy, _, err := parseDefinition(source.dslYaml)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	start, err := parseTime(req.GetStartTime(), "start_time")
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	end, err := parseTime(req.GetEndTime(), "end_time")
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if !end.After(start) {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("end_time 必须晚于 start_time"))}, nil
	}
	if fee := req.GetFeeBps(); math.IsNaN(fee) || math.IsInf(fee, 0) || fee < 0 || fee > store.MaxReplayFeeBps {
		return &strategypb.StartReplayRsp{RetInfo: invalid(fmt.Errorf("fee_bps 必须在 0 到 %d 之间", store.MaxReplayFeeBps))}, nil
	}
	if s.Resolver == nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("Storage 与 Factor 依赖未配置，不能回放"))}, nil
	}
	// 先做一次低成本计数：排队已满时不必再解析绑定、让 Storage 现算覆盖统计；事务内的计数仍是最终判定。
	if active, err := s.Store.CountActiveReplays(ctx, scoped); err != nil {
		return &strategypb.StartReplayRsp{RetInfo: failure(err)}, nil
	} else if active >= store.MaxActiveReplays {
		return &strategypb.StartReplayRsp{RetInfo: invalid(fmt.Errorf("空间 %s 已有 %d 个排队或运行中的回放，请等它们完成或取消后再发起", scoped, active))}, nil
	}
	resolved, program, err := s.Resolver.Resolve(ctx, scoped, req.GetViewId(), strategy)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if !resolved.Spot {
		return &strategypb.StartReplayRsp{RetInfo: invalid(fmt.Errorf("View %s 的源数据集 market_type=%s；第一版回放只支持现货", req.GetViewId(), resolved.MarketType))}, nil
	}
	// A 股日线按上海日期回放：区间两端换成所在上海日期的零点，记录与执行都用换算后的区间。
	if start, end, err = input.ReplayRange(resolved.Calendar, resolved.Bar, start, end); err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	// 同步校验起点、截断终点并检查根数上限：排队后才失败、或回放没有数据的未来 bar 都会误导用户。
	generation := ""
	if end, generation, err = s.Resolver.ReplayWindow(ctx, scoped, resolved, program, start, end); err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	bars, err := input.ReplayBars(resolved.Calendar, resolved.Bar, start, end, input.DefaultReplayMaxBars)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if len(bars) == 0 {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("回放区间内没有完整的 bar"))}, nil
	}
	replayID, err := store.NewID()
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: failure(err)}, nil
	}
	replay := store.Replay{ReplayID: replayID, StrategyID: source.strategyID, InstanceID: source.instanceID, SessionID: source.sessionID, DSLYaml: source.dslYaml, DSLHash: dsl.Hash([]byte(source.dslYaml)), ViewGeneration: generation, Calendar: resolved.Calendar, SpaceID: scoped, ViewID: req.GetViewId(), StartTime: start, EndTime: end, FeeBps: req.GetFeeBps(), Factors: resolved.Factors, CreatedAt: s.nowTime()}
	if err := s.Store.CreateReplay(ctx, replay); err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if s.Replays != nil {
		s.Replays.Wake()
	}
	created, err := s.Store.GetReplay(ctx, replayID)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.StartReplayRsp{RetInfo: success(), Replay: replayProto(created), BarCount: int32(len(bars)), FirstBarEnd: formatTime(bars[0].BarEnd), LastBarEnd: formatTime(bars[len(bars)-1].BarEnd)}, nil
}

// replayInput 是被回放的 DSL 文本及其来源。
type replayInput struct {
	dslYaml    string
	strategyID *string
	instanceID *string
	sessionID  *string
}

// replaySource 按 strategy_id、dsl_yaml、instance_id 三选一确定被回放的 DSL 文本：定义当前的版本、给定文本，
// 或实例当前会话（停用时为最近一次会话）固化的版本——后者与实例在跑或最后在跑的策略一致。
func (s *Service) replaySource(ctx context.Context, scoped string, req *strategypb.StartReplayReq) (replayInput, error) {
	strategyID := strings.TrimSpace(req.GetStrategyId())
	instanceID := strings.TrimSpace(req.GetInstanceId())
	text := req.GetDslYaml()
	given := 0
	for _, value := range []string{strategyID, instanceID, strings.TrimSpace(text)} {
		if value != "" {
			given++
		}
	}
	if given != 1 {
		return replayInput{}, errors.New("strategy_id、dsl_yaml、instance_id 必须且只能给出其一")
	}
	switch {
	case strategyID != "":
		def, err := s.Store.GetDefinition(ctx, strategyID)
		if err != nil || def.DeletedAt != nil {
			return replayInput{}, fmt.Errorf("策略定义 %s 不存在", strategyID)
		}
		return replayInput{dslYaml: def.DSLYaml, strategyID: &def.StrategyID}, nil
	case instanceID != "":
		instance, err := s.Store.GetInstance(ctx, instanceID)
		if err != nil || instance.SpaceID != scoped || instance.DeletedAt != nil {
			return replayInput{}, fmt.Errorf("实例 %s 不存在或不在当前空间", instanceID)
		}
		sessionID := ""
		if instance.SessionID != nil {
			sessionID = *instance.SessionID
		} else if sessions, err := s.Store.ListSessions(ctx, instance.InstanceID); err == nil && len(sessions) > 0 {
			sessionID = sessions[0].SessionID
		}
		if sessionID == "" {
			return replayInput{}, fmt.Errorf("实例 %s 还没有启用过，没有可回放的会话版本", instanceID)
		}
		session, err := s.Store.GetSession(ctx, sessionID)
		if err != nil {
			return replayInput{}, err
		}
		version, err := s.Store.GetDefinitionVersion(ctx, session.DSLHash)
		if err != nil {
			return replayInput{}, err
		}
		return replayInput{dslYaml: version.DSLYaml, strategyID: &instance.StrategyID, instanceID: &instance.InstanceID, sessionID: &sessionID}, nil
	default:
		return replayInput{dslYaml: text}, nil
	}
}

func (s *Service) scopedReplay(ctx context.Context, replayID string) (store.Replay, error) {
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return store.Replay{}, &badRequestError{err}
	}
	if strings.TrimSpace(replayID) == "" {
		return store.Replay{}, &badRequestError{errors.New("replay_id 不能为空")}
	}
	replay, err := s.Store.GetReplay(ctx, replayID)
	if err != nil {
		return store.Replay{}, err
	}
	if replay.SpaceID != scoped {
		return store.Replay{}, &badRequestError{errors.New("回放不在当前空间")}
	}
	return replay, nil
}

// badRequestError 标记请求本身的问题（缺少空间或 ID、回放不在当前空间）。
type badRequestError struct{ error }

func (e *badRequestError) Unwrap() error { return e.error }

// replayLookupError 把读取回放的错误映射为返回码：请求本身的问题 → 参数无效；回放不存在 → NOT_FOUND；
// 其余（存储错误）→ 内部错误，而不是笼统地当作参数无效。
func replayLookupError(err error) *commonpb.RetInfo {
	var bad *badRequestError
	if errors.As(err, &bad) {
		return invalid(err)
	}
	return failure(err)
}

func (s *Service) GetReplay(ctx context.Context, req *strategypb.GetReplayReq) (*strategypb.GetReplayRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetReplayRsp{RetInfo: failure(err)}, nil
	}
	replay, err := s.scopedReplay(ctx, req.GetReplayId())
	if err != nil {
		return &strategypb.GetReplayRsp{RetInfo: replayLookupError(err)}, nil
	}
	return &strategypb.GetReplayRsp{RetInfo: success(), Replay: replayProto(replay)}, nil
}

func (s *Service) ListReplays(ctx context.Context, req *strategypb.ListReplaysReq) (*strategypb.ListReplaysRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListReplaysRsp{RetInfo: failure(err)}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ListReplaysRsp{RetInfo: invalid(err)}, nil
	}
	page, size := pageValues(req.GetPage())
	replays, total, err := s.Store.ListReplays(ctx, scoped, (page-1)*size, size)
	if err != nil {
		return &strategypb.ListReplaysRsp{RetInfo: failure(err)}, nil
	}
	// 列表不带 DSL 全文（存储层不读取），详情用 GetReplay 读取。
	items := make([]*strategypb.Replay, 0, len(replays))
	for _, replay := range replays {
		items = append(items, replayProto(replay))
	}
	return &strategypb.ListReplaysRsp{RetInfo: success(), Replays: items, Total: total, Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) ListReplayBars(ctx context.Context, req *strategypb.ListReplayBarsReq) (*strategypb.ListReplayBarsRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListReplayBarsRsp{RetInfo: failure(err)}, nil
	}
	replay, err := s.scopedReplay(ctx, req.GetReplayId())
	if err != nil {
		return &strategypb.ListReplayBarsRsp{RetInfo: replayLookupError(err)}, nil
	}
	// 完整记录含目标与持仓 JSON，每页最多 100 根，避免报文超过网关的大小上限；brief 只有曲线与摘要字段，每页最多 5000 根。
	page, size := pageValues(req.GetPage())
	limit := 100
	if req.GetBrief() {
		limit = 5000
	}
	if req.GetPage() != nil && req.GetPage().GetPageSize() > 0 {
		size = int(req.GetPage().GetPageSize())
	}
	size = min(size, limit)
	bars, total, err := s.Store.ListReplayBars(ctx, replay.ReplayID, (page-1)*size, size, req.GetBrief())
	if err != nil {
		return &strategypb.ListReplayBarsRsp{RetInfo: failure(err)}, nil
	}
	items := make([]*strategypb.ReplayBar, 0, len(bars))
	for _, bar := range bars {
		items = append(items, replayBarProto(bar))
	}
	return &strategypb.ListReplayBarsRsp{RetInfo: success(), Bars: items, Total: total, Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) CancelReplay(ctx context.Context, req *strategypb.CancelReplayReq) (*strategypb.CancelReplayRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.CancelReplayRsp{RetInfo: failure(err)}, nil
	}
	replay, err := s.scopedReplay(ctx, req.GetReplayId())
	if err != nil {
		return &strategypb.CancelReplayRsp{RetInfo: replayLookupError(err)}, nil
	}
	if err := s.Store.CancelReplay(ctx, replay.ReplayID, s.nowTime()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &strategypb.CancelReplayRsp{RetInfo: invalid(fmt.Errorf("回放 %s 不在可取消的状态", replay.ReplayID))}, nil
		}
		return &strategypb.CancelReplayRsp{RetInfo: failure(err)}, nil
	}
	cancelled, err := s.Store.GetReplay(ctx, replay.ReplayID)
	if err != nil {
		return &strategypb.CancelReplayRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.CancelReplayRsp{RetInfo: success(), Replay: replayProto(cancelled)}, nil
}
