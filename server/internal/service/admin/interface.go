package admin

import (
	"log"

	"github.com/mooyang-code/moox/server/internal/service/admin/config"
	"github.com/mooyang-code/moox/server/internal/service/admin/logic"
	pb "github.com/mooyang-code/moox/server/proto/gen"
)

// AdminService 管理员服务接口
type AdminService interface {
	pb.AdminAPIService
	// 可以添加其他自定义接口
}

// NewAdminService 新建管理员服务
func NewAdminService(cfg *config.Config) (AdminService, error) {
	// 初始化配置
	imp, err := logic.InitAdminServiceImpl(cfg)
	if err != nil {
		log.Fatalf("%+v", err)
	}
	return imp, nil
}
