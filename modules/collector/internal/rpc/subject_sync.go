package rpc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// subjectSyncTimeout 小于路由和监听器的 180 秒预算，保证超时由本服务返回明确的失败结果。
const subjectSyncTimeout = 170 * time.Second

// SubjectSyncer runs a subject sync immediately.
type SubjectSyncer interface {
	SyncNow(ctx context.Context, filter subjectsync.Filter, includeAttributes bool) (subjectsync.Report, error)
	// SupportedListers 返回数据来源能提供标的快照的市场类型。
	SupportedListers() map[string][]string
}

// SetSubjectSyncer wires the syncer shared with the scheduled timers. It is
// called once during bootstrap, before the server starts serving.
func (s *Service) SetSubjectSyncer(syncer SubjectSyncer) { s.subjectSyncer = syncer }

// SyncSubjects synchronizes subject tags (and optionally attributes) now, for
// first deployments and recovery without waiting for the hourly timer. A
// failed tag is reported in the response; the call itself still succeeds so
// the caller sees every tag's outcome.
func (s *Service) SyncSubjects(ctx context.Context, req *pb.SyncSubjectsReq) (*pb.SyncSubjectsRsp, error) {
	if req == nil {
		return &pb.SyncSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	filter := subjectsync.Filter{SpaceID: strings.TrimSpace(req.GetSpaceId()), TagID: strings.TrimSpace(req.GetTagId())}
	if filter.TagID != "" && filter.SpaceID == "" {
		return &pb.SyncSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required with tag_id")}, nil
	}
	if s.subjectSyncer == nil {
		return &pb.SyncSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "subject sync is not configured")}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, subjectSyncTimeout)
	defer cancel()
	report, err := s.subjectSyncer.SyncNow(ctx, filter, req.GetIncludeAttributes())
	if err != nil {
		if errors.Is(err, subjectsync.ErrBusy) {
			return &pb.SyncSubjectsRsp{RetInfo: retErr(pb.ErrorCode_CONFLICT, err.Error())}, nil
		}
		log.ErrorContextf(ctx, "manual subject sync failed: %v", err)
		return &pb.SyncSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.SyncSubjectsRsp{RetInfo: retOK()}
	for _, tag := range report.Tags {
		rsp.Tags = append(rsp.Tags, &pb.SubjectTagSyncResult{SpaceId: tag.SpaceID, TagId: tag.TagID, Status: tag.Status, Subjects: uint32(tag.Subjects), Message: tag.Message})
	}
	for _, attribute := range report.Attributes {
		rsp.Attributes = append(rsp.Attributes, &pb.SubjectAttributeSyncResult{SpaceId: attribute.SpaceID, Status: attribute.Status, Message: attribute.Message})
	}
	if len(rsp.Tags) == 0 && len(rsp.Attributes) == 0 {
		rsp.RetInfo = retErr(pb.ErrorCode_NOT_FOUND, "no tag or attribute job matches the filter")
	}
	return rsp, nil
}

// ListTagSyncStatus 返回标签的标的同步运行结果（Collector 自己的库）。没有运行记录的标签不在其中，
// 调用方（Monitor、管理台）把它们按“从未运行”处理。
func (s *Service) ListTagSyncStatus(ctx context.Context, req *pb.ListTagSyncStatusReq) (*pb.ListTagSyncStatusRsp, error) {
	if s.persistence == nil || s.persistence.TagSyncRuns() == nil {
		return &pb.ListTagSyncStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "tag sync status is not configured")}, nil
	}
	runs, err := s.persistence.TagSyncRuns().List(ctx, strings.TrimSpace(req.GetSpaceId()))
	if err != nil {
		return &pb.ListTagSyncStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListTagSyncStatusRsp{RetInfo: retOK()}
	for _, run := range runs {
		item := &pb.TagSyncStatus{SpaceId: run.SpaceID, TagId: run.TagID, LastRunAt: run.LastRunAt.Format(time.RFC3339), LastStatus: run.LastStatus, LastError: run.LastError}
		if !run.LastSuccessAt.IsZero() {
			item.LastSuccessAt = run.LastSuccessAt.Format(time.RFC3339)
		}
		rsp.Items = append(rsp.Items, item)
	}
	return rsp, nil
}

// ListSubjectListers 返回能提供标的快照的（数据来源，市场类型）组合，管理台新建自动标签时据此给出选项。
func (s *Service) ListSubjectListers(ctx context.Context, _ *pb.ListSubjectListersReq) (*pb.ListSubjectListersRsp, error) {
	if s.subjectSyncer == nil {
		return &pb.ListSubjectListersRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "subject sync is not configured")}, nil
	}
	supported := s.subjectSyncer.SupportedListers()
	sources := make([]string, 0, len(supported))
	for source := range supported {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	rsp := &pb.ListSubjectListersRsp{RetInfo: retOK()}
	for _, source := range sources {
		rsp.Listers = append(rsp.Listers, &pb.SubjectLister{Source: source, MarketTypes: append([]string(nil), supported[source]...)})
	}
	return rsp, nil
}
