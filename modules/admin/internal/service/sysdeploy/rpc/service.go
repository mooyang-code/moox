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

func (s *Service) ListGatewayNodes(ctx context.Context, req *pb.ListGatewayNodesReq) (*pb.ListGatewayNodesRsp, error) {
	return s.svc.ListGatewayNodes(ctx, req)
}
func (s *Service) CreateGatewayNode(ctx context.Context, req *pb.CreateGatewayNodeReq) (*pb.CreateGatewayNodeRsp, error) {
	return s.svc.CreateGatewayNode(ctx, req)
}
func (s *Service) UpdateGatewayNode(ctx context.Context, req *pb.UpdateGatewayNodeReq) (*pb.UpdateGatewayNodeRsp, error) {
	return s.svc.UpdateGatewayNode(ctx, req)
}
func (s *Service) DeleteGatewayNode(ctx context.Context, req *pb.DeleteGatewayNodeReq) (*pb.DeleteGatewayNodeRsp, error) {
	return s.svc.DeleteGatewayNode(ctx, req)
}
func (s *Service) GetGatewayNodeRoutes(ctx context.Context, req *pb.GetGatewayNodeRoutesReq) (*pb.GetGatewayNodeRoutesRsp, error) {
	return s.svc.GetGatewayNodeRoutes(ctx, req)
}
