package rpc

import (
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

// Service is the independent cloudnode service implementation.
type Service struct {
	pb.UnimplementedCloudNodeMgr
	dbm          *storage.Manager
	workItemRepo *repository.WorkItemRepository
	catalog      *repository.CatalogRepository
}

// New creates a cloudnode RPC service.
func New(dbm *storage.Manager) *Service {
	return &Service{
		dbm:          dbm,
		workItemRepo: repository.NewWorkItemRepository(dbm.DB()),
		catalog:      repository.NewCatalogRepository(dbm.DB()),
	}
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}
