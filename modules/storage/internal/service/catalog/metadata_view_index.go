package catalog

import (
	"context"
	"crypto/hmac"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// CompleteViewInitialBuild 提交首次建索引的结果，View 从此可读。
func (s *Service) CompleteViewInitialBuild(ctx context.Context, req *pb.CompleteViewInitialBuildReq) (*pb.CompleteViewInitialBuildRsp, error) {
	if req == nil {
		return &pb.CompleteViewInitialBuildRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateViewServiceAuth(req.GetAuthInfo()); err != nil {
		return &pb.CompleteViewInitialBuildRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	view, err := s.metadata.CompleteViewInitialBuild(ctx, req)
	if err != nil {
		return &pb.CompleteViewInitialBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CompleteViewInitialBuildRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CompleteViewInitialBuildRsp{RetInfo: retinfo.Success("success"), View: view}, nil
}

func (s *Service) CommitViewSchemaExtension(ctx context.Context, req *pb.CommitViewSchemaExtensionReq) (*pb.CommitViewSchemaExtensionRsp, error) {
	if req == nil {
		return &pb.CommitViewSchemaExtensionRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateViewServiceAuth(req.GetAuthInfo()); err != nil {
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

// validateViewServiceAuth 校验 storage-view 的服务签名：storage-view 直连 Metadata，这些方法只给它用。
func (s *Service) validateViewServiceAuth(auth *pb.AuthInfo) error {
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
