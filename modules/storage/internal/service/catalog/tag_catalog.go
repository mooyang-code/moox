package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) UpsertTag(ctx context.Context, req *pb.UpsertTagReq) (*pb.UpsertTagRsp, error) {
	if req == nil || req.GetTag() == nil {
		return &pb.UpsertTagRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("tag is required"))}, nil
	}
	if _, err := s.metadata.GetTag(ctx, req.GetTag().GetSpaceId(), req.GetTag().GetTagId()); errors.Is(err, sql.ErrNoRows) {
		req.GetTag().Builtin = false
	}
	tag, err := s.metadata.UpsertTag(ctx, req.GetTag())
	if err != nil {
		return &pb.UpsertTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "UpsertTag")
	return &pb.UpsertTagRsp{RetInfo: retinfo.Success("success"), Tag: tag}, nil
}

func (s *Service) GetTag(ctx context.Context, req *pb.GetTagReq) (*pb.GetTagRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return &pb.GetTagRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_id are required"))}, nil
	}
	tag, err := s.metadata.GetTag(ctx, req.GetSpaceId(), req.GetTagId())
	if err != nil {
		return &pb.GetTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetTagRsp{RetInfo: retinfo.Success("success"), Tag: tag}, nil
}

func (s *Service) ListTags(ctx context.Context, req *pb.ListTagsReq) (*pb.ListTagsRsp, error) {
	var spaceID string
	var page *pb.Page
	if req != nil {
		spaceID, page = req.GetSpaceId(), req.GetPage()
	}
	tags, result, err := s.metadata.ListTags(ctx, spaceID, page)
	if err != nil {
		return &pb.ListTagsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListTagsRsp{RetInfo: retinfo.Success("success"), Tags: tags, PageResult: result}, nil
}

func (s *Service) DeleteTag(ctx context.Context, req *pb.DeleteTagReq) (*pb.DeleteTagRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return &pb.DeleteTagRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_id are required"))}, nil
	}
	err := s.metadata.DeleteTag(ctx, req.GetSpaceId(), req.GetTagId())
	if err != nil {
		rsp := &pb.DeleteTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}
		var referenced *metadatastore.TagReferencedError
		if errors.As(err, &referenced) {
			rsp.References = referenced.References
		}
		return rsp, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteTag")
	return &pb.DeleteTagRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ListTagMembers(ctx context.Context, req *pb.ListTagMembersReq) (*pb.ListTagMembersRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ListTagMembersRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	members, result, err := s.metadata.ListTagMembers(ctx, metadatastore.TagMemberQuery{SpaceID: req.GetSpaceId(), TagID: req.GetTagId(), Status: req.GetStatus(), Keyword: req.GetKeyword(), Page: req.GetPage()})
	if err != nil {
		return &pb.ListTagMembersRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListTagMembersRsp{RetInfo: retinfo.Success("success"), Members: members, PageResult: result}, nil
}

func (s *Service) AddTagMembers(ctx context.Context, req *pb.TagMembersReq) (*pb.TagMembersRsp, error) {
	affected, err := s.changeTagMembers(ctx, req, func(spaceID, tagID string, ids []string) (int, error) {
		return s.metadata.AddTagMembers(ctx, spaceID, tagID, ids)
	})
	return tagMembersRsp(affected, err), nil
}

func (s *Service) RemoveTagMembers(ctx context.Context, req *pb.TagMembersReq) (*pb.TagMembersRsp, error) {
	affected, err := s.changeTagMembers(ctx, req, func(spaceID, tagID string, ids []string) (int, error) {
		return s.metadata.RemoveTagMembers(ctx, spaceID, tagID, ids)
	})
	return tagMembersRsp(affected, err), nil
}

func (s *Service) SetTagMemberStatus(ctx context.Context, req *pb.SetTagMemberStatusReq) (*pb.TagMembersRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return &pb.TagMembersRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_id are required"))}, nil
	}
	affected, err := s.metadata.SetTagMemberStatus(ctx, req.GetSpaceId(), req.GetTagId(), req.GetSubjectIds(), req.GetStatus())
	return tagMembersRsp(affected, err), nil
}

func (s *Service) changeTagMembers(ctx context.Context, req *pb.TagMembersReq, fn func(string, string, []string) (int, error)) (int, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return 0, errors.New("space_id and tag_id are required")
	}
	return fn(req.GetSpaceId(), req.GetTagId(), req.GetSubjectIds())
}

func tagMembersRsp(affected int, err error) *pb.TagMembersRsp {
	if err != nil {
		return &pb.TagMembersRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}
	}
	return &pb.TagMembersRsp{RetInfo: retinfo.Success("success"), Affected: uint32(affected)}
}

func (s *Service) ApplyTagSnapshot(ctx context.Context, req *pb.ApplyTagSnapshotReq) (*pb.ApplyTagSnapshotRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_id are required"))}, nil
	}
	runAt, err := parseTagRunAt(req.GetRunAt())
	if err != nil {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	result, err := s.metadata.ApplyTagSnapshot(ctx, req.GetSpaceId(), req.GetTagId(), runAt, req.GetItems())
	if err != nil {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "ApplyTagSnapshot")
	return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Success("success"), Added: uint32(result.Added), Activated: uint32(result.Activated), Inactivated: uint32(result.Inactivated)}, nil
}

func (s *Service) ReportTagRunFailure(ctx context.Context, req *pb.ReportTagRunFailureReq) (*pb.ReportTagRunFailureRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTagId()) == "" {
		return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_id are required"))}, nil
	}
	runAt, err := parseTagRunAt(req.GetRunAt())
	if err != nil {
		return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.metadata.ReportTagRunFailure(ctx, req.GetSpaceId(), req.GetTagId(), runAt, req.GetError()); err != nil {
		return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "ReportTagRunFailure")
	return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) UpdateSubjectAttributes(ctx context.Context, req *pb.UpdateSubjectAttributesReq) (*pb.UpdateSubjectAttributesRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.UpdateSubjectAttributesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	updated, skipped, err := s.metadata.UpdateSubjectAttributes(ctx, req.GetSpaceId(), req.GetItems())
	if err != nil {
		return &pb.UpdateSubjectAttributesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "UpdateSubjectAttributes")
	return &pb.UpdateSubjectAttributesRsp{RetInfo: retinfo.Success("success"), Updated: uint32(updated), Skipped: uint32(skipped)}, nil
}

func (s *Service) ResolveSubjects(ctx context.Context, req *pb.ResolveSubjectsReq) (*pb.ResolveSubjectsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	items, err := s.metadata.ResolveSubjects(ctx, req.GetSpaceId(), req.GetTagIds())
	if err != nil {
		return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Success("success"), Subjects: items}, nil
}

func parseTagRunAt(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("run_at must be RFC3339 or UTC SQL time")
}
