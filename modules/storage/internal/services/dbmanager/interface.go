package dbmanager

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/dbmanager/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/dbmanager/logic"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// DBTableManagerService 管理员服务接口
type DBTableManagerService interface {
	pb.DBTableManagerService
	// 可以添加其他自定义接口
}

// NewDBTableManagerService 新建管理员服务
func NewDBTableManagerService(cfg *config.Config) (DBTableManagerService, error) {
	// 初始化配置
	imp, err := logic.InitDBTableManagerServiceImpl(cfg)
	if err != nil {
		log.Fatalf("初始化admin服务失败: %+v", err)
	}
	return imp, nil
}
