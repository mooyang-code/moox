package catalog

import (
	"context"
	"crypto/hmac"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ClaimViewIndexBuildRsp, error) {
	build, resumed, err := s.metadata.ClaimViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.ClaimViewIndexBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.ClaimViewIndexBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ClaimViewIndexBuildRsp{RetInfo: retinfo.Success("success"), View: view, Build: build, Resumed: resumed}, nil
}

func (s *Service) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.UpdateViewIndexBuildRsp, error) {
	build, err := s.metadata.UpdateViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.UpdateViewIndexBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateViewIndexBuildRsp{RetInfo: retinfo.Success("success"), Build: build}, nil
}

func (s *Service) ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.ActivateViewIndexRsp, error) {
	view, err := s.metadata.ActivateViewIndex(ctx, req)
	if err != nil {
		return &pb.ActivateViewIndexRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.ActivateViewIndexRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ActivateViewIndexRsp{RetInfo: retinfo.Success("success"), View: view}, nil
}

func (s *Service) CommitViewSchemaExtension(ctx context.Context, req *pb.CommitViewSchemaExtensionReq) (*pb.CommitViewSchemaExtensionRsp, error) {
	if req == nil {
		return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateViewSchemaExtensionAuth(req.GetAuthInfo()); err != nil {
		return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	view, err := s.metadata.CommitViewSchemaExtension(ctx, req)
	if err != nil {
		return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("View schema committed but metadata publication is pending; retry"))}, nil
	}
	return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Success("success"), View: view}, nil
}

func (s *Service) validateViewSchemaExtensionAuth(auth *pb.AuthInfo) error {
	if strings.TrimSpace(s.viewAuthSecret) == "" {
		return errors.New("storage View auth secret is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("View service auth is required")
	}
	if auth.GetAppId() != "storage-view" {
		return errors.New("storage-view service identity required")
	}
	expected := serviceAuthKey(s.viewAuthSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(auth.GetAppKey()))), []byte(expected)) {
		return errors.New("invalid View service HMAC")
	}
	return nil
}

func (s *Service) FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.FailViewIndexBuildRsp, error) {
	build, err := s.metadata.FailViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.FailViewIndexBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.FailViewIndexBuildRsp{RetInfo: retinfo.Success("success"), Build: build}, nil
}
