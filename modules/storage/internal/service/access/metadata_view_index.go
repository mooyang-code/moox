package access

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ClaimViewIndexBuildRsp, error) {
	build, resumed, err := s.metadata.ClaimViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.ClaimViewIndexBuildRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.ClaimViewIndexBuildRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ClaimViewIndexBuildRsp{RetInfo: response.Success("success"), View: view, Build: build, Resumed: resumed}, nil
}

func (s *Service) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.UpdateViewIndexBuildRsp, error) {
	build, err := s.metadata.UpdateViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.UpdateViewIndexBuildRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateViewIndexBuildRsp{RetInfo: response.Success("success"), Build: build}, nil
}

func (s *Service) ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.ActivateViewIndexRsp, error) {
	view, err := s.metadata.ActivateViewIndex(ctx, req)
	if err != nil {
		return &pb.ActivateViewIndexRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.ActivateViewIndexRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ActivateViewIndexRsp{RetInfo: response.Success("success"), View: view}, nil
}

func (s *Service) FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.FailViewIndexBuildRsp, error) {
	build, err := s.metadata.FailViewIndexBuild(ctx, req)
	if err != nil {
		return &pb.FailViewIndexBuildRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.FailViewIndexBuildRsp{RetInfo: response.Success("success"), Build: build}, nil
}
