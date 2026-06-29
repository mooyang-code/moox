// Package rpc exposes SysDeploy through tRPC while keeping business logic in sysdeploy.Service.
package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Service is a thin RPC adapter for service deployment management.
type Service struct {
	pb.UnimplementedSysDeploy
	svc sysdeploy.Service
}

func NewService(svc sysdeploy.Service) *Service {
	return &Service{svc: svc}
}

func (s *Service) ListServiceDeployments(ctx context.Context, req *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error) {
	return s.svc.ListServiceDeployments(ctx, req)
}

func (s *Service) GetServiceDeployment(ctx context.Context, req *pb.GetServiceDeploymentReq) (*pb.GetServiceDeploymentRsp, error) {
	return s.svc.GetServiceDeployment(ctx, req)
}

func (s *Service) CreateServiceDeployment(ctx context.Context, req *pb.CreateServiceDeploymentReq) (*pb.CreateServiceDeploymentRsp, error) {
	return s.svc.CreateServiceDeployment(ctx, req)
}

func (s *Service) UpdateServiceDeployment(ctx context.Context, req *pb.UpdateServiceDeploymentReq) (*pb.UpdateServiceDeploymentRsp, error) {
	return s.svc.UpdateServiceDeployment(ctx, req)
}

func (s *Service) DeleteServiceDeployment(ctx context.Context, req *pb.DeleteServiceDeploymentReq) (*pb.DeleteServiceDeploymentRsp, error) {
	return s.svc.DeleteServiceDeployment(ctx, req)
}

func (s *Service) ListActiveServiceDeployments(ctx context.Context, req *pb.ListActiveServiceDeploymentsReq) (*pb.ListActiveServiceDeploymentsRsp, error) {
	return s.svc.ListActiveServiceDeployments(ctx, req)
}
