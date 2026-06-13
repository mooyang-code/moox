package adapter

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/logic"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Adapter 服务总接口
type Adapter interface {
	pb.AdapterService
	// 可以添加其他自定义接口
}

// NewAdapter 新建存储适配层服务
func NewAdapter(cfg *config.Config) (Adapter, error) {
	return logic.InitAdapterImpl(cfg)
}
