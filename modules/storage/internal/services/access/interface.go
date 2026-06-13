package access

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/access/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/access/logic"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Accessor 存储接入层服务总接口
type Accessor interface {
	pb.AccessService
	// 可以添加其他自定义接口
}

// NewAccessor 新建存储接入层服务
func NewAccessor(cfg *config.Config) (Accessor, error) {
	// 初始化配置
	imp, err := logic.InitAccessorImpl(cfg)
	if err != nil {
		return nil, err
	}
	return imp, nil
}

// SetLocalAdapterService 注册本地适配层服务实例，用于同进程直连
func SetLocalAdapterService(svc pb.AdapterService) {
	logic.SetLocalAdapterService(svc)
}
