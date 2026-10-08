package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

func parseTime(raw, field string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s 不能为空", field)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at.UTC(), nil
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
	dslYaml := req.GetDslYaml()
	var strategyID *string
	if id := strings.TrimSpace(req.GetStrategyId()); id != "" {
		def, err := s.Store.GetDefinition(ctx, id)
		if err != nil || def.DeletedAt != nil {
			return &strategypb.StartReplayRsp{RetInfo: invalid(fmt.Errorf("策略定义 %s 不存在", id))}, nil
		}
		if strings.TrimSpace(dslYaml) == "" {
			dslYaml = def.DSLYaml
		}
		strategyID = &def.StrategyID
	}
	strategy, _, err := parseDefinition(dslYaml)
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
	if req.GetFeeBps() < 0 {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("fee_bps 不能为负"))}, nil
	}
	if s.Resolver == nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(errors.New("Storage 与 Factor 依赖未配置，不能回放"))}, nil
	}
	resolved, _, err := s.Resolver.Resolve(ctx, scoped, req.GetViewId(), strategy)
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: invalid(err)}, nil
	}
	if !resolved.Spot {
		return &strategypb.StartReplayRsp{RetInfo: invalid(fmt.Errorf("View %s 的源数据集 market_type=%s；第一版回放只支持现货", req.GetViewId(), resolved.MarketType))}, nil
	}
	replayID, err := store.NewID()
	if err != nil {
		return &strategypb.StartReplayRsp{RetInfo: failure(err)}, nil
	}
	replay := store.Replay{ReplayID: replayID, StrategyID: strategyID, DSLYaml: dslYaml, SpaceID: scoped, ViewID: req.GetViewId(), StartTime: start, EndTime: end, FeeBps: req.GetFeeBps(), CreatedAt: s.nowTime()}
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
	return &strategypb.StartReplayRsp{RetInfo: success(), Replay: replayProto(created)}, nil
}

func (s *Service) scopedReplay(ctx context.Context, replayID string) (store.Replay, error) {
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return store.Replay{}, err
	}
	if strings.TrimSpace(replayID) == "" {
		return store.Replay{}, errors.New("replay_id 不能为空")
	}
	replay, err := s.Store.GetReplay(ctx, replayID)
	if err != nil {
		return store.Replay{}, err
	}
	if replay.SpaceID != scoped {
		return store.Replay{}, errors.New("回放不在当前空间")
	}
	return replay, nil
}

func (s *Service) GetReplay(ctx context.Context, req *strategypb.GetReplayReq) (*strategypb.GetReplayRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetReplayRsp{RetInfo: failure(err)}, nil
	}
	replay, err := s.scopedReplay(ctx, req.GetReplayId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &strategypb.GetReplayRsp{RetInfo: failure(err)}, nil
		}
		return &strategypb.GetReplayRsp{RetInfo: invalid(err)}, nil
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
		return &strategypb.ListReplayBarsRsp{RetInfo: invalid(err)}, nil
	}
	page, size := pageValues(req.GetPage())
	if req.GetPage() == nil || req.GetPage().GetPageSize() <= 0 {
		size = 500
	}
	bars, total, err := s.Store.ListReplayBars(ctx, replay.ReplayID, (page-1)*size, size)
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
		return &strategypb.CancelReplayRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.Store.CancelReplay(ctx, replay.ReplayID, s.nowTime()); err != nil {
		return &strategypb.CancelReplayRsp{RetInfo: invalid(fmt.Errorf("回放 %s 不在可取消的状态", replay.ReplayID))}, nil
	}
	cancelled, err := s.Store.GetReplay(ctx, replay.ReplayID)
	if err != nil {
		return &strategypb.CancelReplayRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.CancelReplayRsp{RetInfo: success(), Replay: replayProto(cancelled)}, nil
}
