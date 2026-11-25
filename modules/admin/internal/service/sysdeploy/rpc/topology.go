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

func (s *Service) ListDeployments(ctx context.Context, req *pb.ListDeploymentsReq) (*pb.ListDeploymentsRsp, error) {
	return s.svc.ListDeployments(ctx, req)
}

func (s *Service) GetHostRoutes(ctx context.Context, req *pb.GetHostRoutesReq) (*pb.GetHostRoutesRsp, error) {
	return s.svc.GetHostRoutes(ctx, req)
}

func (s *Service) SyncTopology(ctx context.Context, req *pb.SyncTopologyReq) (*pb.SyncTopologyRsp, error) {
	return s.svc.SyncTopology(ctx, req)
}
