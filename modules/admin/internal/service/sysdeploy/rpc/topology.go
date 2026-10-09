package rpc

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

func (s *Service) GetCatalog(ctx context.Context, req *pb.GetCatalogReq) (*pb.GetCatalogRsp, error) {
	return s.svc.GetCatalog(ctx, req)
}

func (s *Service) ListHosts(ctx context.Context, req *pb.ListDeploymentHostsReq) (*pb.ListDeploymentHostsRsp, error) {
	return s.svc.ListHosts(ctx, req)
}

func (s *Service) ListPlacements(ctx context.Context, req *pb.ListPlacementsReq) (*pb.ListPlacementsRsp, error) {
	return s.svc.ListPlacements(ctx, req)
}

func (s *Service) GetHostRoutes(ctx context.Context, req *pb.GetHostRoutesReq) (*pb.GetHostRoutesRsp, error) {
	return s.svc.GetHostRoutes(ctx, req)
}

func (s *Service) GetDirectory(ctx context.Context, req *pb.GetDirectoryReq) (*pb.GetDirectoryRsp, error) {
	return s.svc.GetDirectory(ctx, req)
}

func (s *Service) SetHostStatus(ctx context.Context, req *pb.SetHostStatusReq) (*pb.SetHostStatusRsp, error) {
	return s.svc.SetHostStatus(ctx, req)
}

func (s *Service) SetPlacementStatus(ctx context.Context, req *pb.SetPlacementStatusReq) (*pb.SetPlacementStatusRsp, error) {
	return s.svc.SetPlacementStatus(ctx, req)
}

func (s *Service) SyncHostPlacements(ctx context.Context, req *pb.SyncHostPlacementsReq) (*pb.SyncHostPlacementsRsp, error) {
	return s.svc.SyncHostPlacements(ctx, req)
}

func (s *Service) DeleteHost(ctx context.Context, req *pb.DeleteDeploymentHostReq) (*pb.DeleteDeploymentHostRsp, error) {
	return s.svc.DeleteHost(ctx, req)
}
