package csv

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// init 包初始化函数，将 CSV 注册到设备系统
func init() {
	dao.RegisterDeviceType(pb.EnumDeviceType_CSV_DEVICE, func(ctx context.Context) (dao.Storer, error) {
		return &CSV{}, nil
	})
}
