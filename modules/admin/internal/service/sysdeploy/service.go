package sysdeploy

import (
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
)

type Service interface{ pb.SysDeployService }

type ServiceImpl struct {
	pb.UnimplementedSysDeploy
	db          *gorm.DB
	adminNodeID string
}

func NewService(dbManager *database.Manager, adminNodeID string) *ServiceImpl {
	return &ServiceImpl{db: dbManager.GetDB(), adminNodeID: strings.TrimSpace(adminNodeID)}
}

func makePageResult(pageNo, size int, total int64) *pb.PageResult {
	return &pb.PageResult{Page: uint32(pageNo), Size: uint32(size), Total: uint32(total), HasMore: int64(pageNo*size) < total}
}
func retOK() *pb.RetInfo                               { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"} }
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo { return &pb.RetInfo{Code: code, Msg: msg} }
