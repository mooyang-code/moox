package rpc

import (
	"context"
	"errors"
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
