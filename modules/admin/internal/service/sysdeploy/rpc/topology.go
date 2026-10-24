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

func (s *Service) GetDirectory(ctx context.Context, req *pb.GetDirectoryReq) (*pb.GetDirectoryRsp, error) {
	return s.svc.GetDirectory(ctx, req)
}

func (s *Service) SetHostStatus(ctx context.Context, req *pb.SetHostStatusReq) (*pb.SetHostStatusRsp, error) {
	return s.svc.SetHostStatus(ctx, req)
}

func (s *Service) SetDeploymentStatus(ctx context.Context, req *pb.SetDeploymentStatusReq) (*pb.SetDeploymentStatusRsp, error) {
	return s.svc.SetDeploymentStatus(ctx, req)
}

func (s *Service) SyncHostDeployments(ctx context.Context, req *pb.SyncHostDeploymentsReq) (*pb.SyncHostDeploymentsRsp, error) {
	return s.svc.SyncHostDeployments(ctx, req)
}

func (s *Service) DeleteHost(ctx context.Context, req *pb.DeleteDeploymentHostReq) (*pb.DeleteDeploymentHostRsp, error) {
	return s.svc.DeleteHost(ctx, req)
}

func (s *Service) InspectEventBus(ctx context.Context, req *pb.InspectEventBusReq) (*pb.InspectEventBusRsp, error) {
	return s.svc.InspectEventBus(ctx, req)
}

func (s *Service) DeleteEventBusConsumers(ctx context.Context, req *pb.DeleteEventBusConsumersReq) (*pb.DeleteEventBusConsumersRsp, error) {
	return s.svc.DeleteEventBusConsumers(ctx, req)
}

func (s *Service) PurgeEventBusSubjects(ctx context.Context, req *pb.PurgeEventBusSubjectsReq) (*pb.PurgeEventBusSubjectsRsp, error) {
	return s.svc.PurgeEventBusSubjects(ctx, req)
}
