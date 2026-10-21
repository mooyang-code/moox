package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// maxExclusionSubjects 是一次设置或移除的标的数上限。
const maxExclusionSubjects = 1000

// activityLogger 写入全局活动日志（marketfetch.ActivityLogger）。
type activityLogger = marketfetch.ActivityLogger

// SetActivityLogger 注入全局活动日志（采集名单干预写 collector.subject_change）。
func (s *Service) SetActivityLogger(activity marketfetch.ActivityLogger) { s.activity = activity }

// SetSubjectExclusions 按空间加入一批排除：下一轮对账（≤1 分钟）生成新计划，按提前量生效。
func (s *Service) SetSubjectExclusions(ctx context.Context, req *pb.SetSubjectExclusionsReq) (*pb.SetSubjectExclusionsRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	subjects, err := exclusionSubjects(spaceID, req.GetSubjectIds())
	if err != nil {
		return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "reason 必填：写明排除原因（公告下架、数据异常等）")}, nil
	}
	now := time.Now().UTC()
	var expires time.Time
	if raw := strings.TrimSpace(req.GetExpiresAt()); raw != "" {
		if expires, err = time.Parse(time.RFC3339, raw); err != nil {
			return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "expires_at 必须是 RFC3339 时间")}, nil
		}
		if !expires.After(now) {
			return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "expires_at 必须晚于当前时间")}, nil
		}
	}
	operator := exclusionOperator(req.GetOperator())
	items := make([]store.SubjectExclusion, 0, len(subjects))
	for _, subject := range subjects {
		items = append(items, store.SubjectExclusion{SpaceID: spaceID, SubjectID: subject, MarketType: req.GetMarketType(), Reason: reason, Caller: operator, CreatedAt: now, ExpiresAt: expires})
	}
	if err := s.persistence.SubjectExclusions().Set(ctx, items); err != nil {
		return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	market := strings.ToLower(strings.TrimSpace(req.GetMarketType()))
	title := fmt.Sprintf("采集名单排除 %d 个标的（%s）：%s", len(subjects), marketLabel(market), reason)
	s.logExclusion(ctx, spaceID, "set", title, map[string]any{"subject_ids": subjects, "market_type": market, "reason": reason, "operator": operator, "expires_at": req.GetExpiresAt()}, now)
	exclusions, err := s.activeExclusions(ctx, spaceID)
	if err != nil {
		return &pb.SetSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.SetSubjectExclusionsRsp{RetInfo: retOK(), Exclusions: exclusions}, nil
}

// ClearSubjectExclusions 按空间与标的移除排除：标的从下一份计划起回到采集（期间的历史需要时用补采）。
func (s *Service) ClearSubjectExclusions(ctx context.Context, req *pb.ClearSubjectExclusionsReq) (*pb.ClearSubjectExclusionsRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	subjects, err := exclusionSubjects(spaceID, req.GetSubjectIds())
	if err != nil {
		return &pb.ClearSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	removed, err := s.persistence.SubjectExclusions().Clear(ctx, spaceID, subjects, req.GetMarketType())
	if err != nil {
		return &pb.ClearSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if removed > 0 {
		market := strings.ToLower(strings.TrimSpace(req.GetMarketType()))
		operator := exclusionOperator(req.GetOperator())
		title := fmt.Sprintf("采集名单解除排除 %d 个标的（%s）", len(subjects), marketLabel(market))
		s.logExclusion(ctx, spaceID, "clear", title, map[string]any{"subject_ids": subjects, "market_type": market, "operator": operator, "removed": removed}, time.Now().UTC())
	}
	return &pb.ClearSubjectExclusionsRsp{RetInfo: retOK(), Removed: removed}, nil
}

// ListSubjectExclusions 列出空间当前生效的排除。
func (s *Service) ListSubjectExclusions(ctx context.Context, req *pb.ListSubjectExclusionsReq) (*pb.ListSubjectExclusionsRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.ListSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	exclusions, err := s.activeExclusions(ctx, spaceID)
	if err != nil {
		return &pb.ListSubjectExclusionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.ListSubjectExclusionsRsp{RetInfo: retOK(), Exclusions: exclusions}, nil
}

func (s *Service) activeExclusions(ctx context.Context, spaceID string) ([]*pb.SubjectExclusion, error) {
	items, err := s.persistence.SubjectExclusions().Active(ctx, spaceID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	result := make([]*pb.SubjectExclusion, 0, len(items))
	for _, item := range items {
		entry := &pb.SubjectExclusion{SpaceId: item.SpaceID, SubjectId: item.SubjectID, MarketType: item.MarketType, Reason: item.Reason, Operator: item.Caller, CreatedAt: item.CreatedAt.Format(time.RFC3339)}
		if !item.ExpiresAt.IsZero() {
			entry.ExpiresAt = item.ExpiresAt.Format(time.RFC3339)
		}
		result = append(result, entry)
	}
	return result, nil
}

// logExclusion 写活动日志 collector.subject_change；写入失败只记日志，排除已经生效。
func (s *Service) logExclusion(ctx context.Context, spaceID, action, title string, detail map[string]any, at time.Time) {
	if s.activity == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	entry := marketfetch.ActivityEntry{
		SpaceID: spaceID, Source: "collector", Category: "collector.subject_change", Level: "info",
		Title: title, Detail: string(raw), DedupKey: fmt.Sprintf("collector.subject_exclusion.%s.%s.%d", spaceID, action, at.UnixNano()),
	}
	if err := s.activity.AppendActivityLogs(ctx, spaceID, []marketfetch.ActivityEntry{entry}); err != nil {
		log.WarnContextf(ctx, "collector subject exclusion activity log not written space=%s action=%s: %v", spaceID, action, err)
	}
}

func exclusionSubjects(spaceID string, raw []string) ([]string, error) {
	if spaceID == "" {
		return nil, fmt.Errorf("space_id 必填")
	}
	seen := map[string]bool{}
	subjects := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		subjects = append(subjects, value)
	}
	if len(subjects) == 0 {
		return nil, fmt.Errorf("subject_ids 至少一个")
	}
	if len(subjects) > maxExclusionSubjects {
		return nil, fmt.Errorf("一次最多 %d 个标的", maxExclusionSubjects)
	}
	return subjects, nil
}

func exclusionOperator(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

func marketLabel(market string) string {
	if market == "" {
		return "全部市场"
	}
	return market
}
