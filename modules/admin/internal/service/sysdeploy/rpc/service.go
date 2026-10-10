// Package rpc exposes SysDeploy through tRPC while keeping business logic in sysdeploy.Service.
package rpc

import (
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
