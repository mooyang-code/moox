// Package bleve Bleve相关逻辑
package bleve

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// ============================================================================
// 初始化函数 - 包初始化和构造函数
// ============================================================================

// init 包初始化函数，将 Bleve 注册到设备系统
func init() {
	// 注册 Bleve 设备类型
	dao.RegisterDeviceType(pb.EnumDeviceType_BLEVE_DEVICE, func(_ context.Context) (dao.Storer, error) {
		return NewBleve(), nil
	})
}

// NewBleve 创建新的Bleve存储对象
func NewBleve() *Bleve {
	return &Bleve{
		isGetAll: false,
		data:     make(map[string]any),
		infos:    make(map[uint32]*pb.ModifyFieldInfo),
	}
}
