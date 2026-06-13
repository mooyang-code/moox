package metadata

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/api"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/logic"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/server"
)

// MetaServicer 服务总接口
type MetaServicer interface {
	pb.MetaFieldService
	pb.MetaAdminService
}

// NewMetaServicer 新建元数据字段服务
func NewMetaServicer(s *server.Server) (MetaServicer, error) {
	svc, err := logic.NewMetaServicerImpl()
	if err != nil {
		return nil, err
	}

	// 注册API处理器
	api.RegisterStandardHTTPHandlers(s)
	return svc, nil
}
