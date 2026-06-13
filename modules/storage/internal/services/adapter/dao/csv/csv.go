// Package csv 提供CSV文件存储适配器，用于离线数据分析场景下的时序数据存储
package csv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// CSV CSV存储适配器
// 用途：离线数据分析场景下的数据存储
// 特点：
// 1. 主要用于时序数据的顺序写入
// 2. 典型应用：在线系统变更日志的离线同步
// 3. 仅支持顺序插入操作，不支持查询和更新
// 4. 适用于大数据量的批量写入场景
type CSV struct {
	connInfo string // 存储CSV文件的根目录路径
}

// GetDeviceTableID 根据逻辑表ID获得底层物理设备表名
func (c *CSV) GetDeviceTableID(logicTableID string) string {
	// CSV适配器直接使用逻辑表ID作为文件名
	return logicTableID
}

// GetDeviceConn 根据存储对象信息进行连接操作
func (c *CSV) GetDeviceConn(connectInfo string) error {
	ctx := context.Background()
	log.DebugContextf(ctx, "csv connectInfo is %s", connectInfo)

	// 处理connectInfo为localhost的情况，使用配置文件中的路径
	actualConnectInfo := connectInfo
	if connectInfo == "localhost" {
		cfg := config.GetGlobalConfig()
		if cfg != nil && cfg.CSV.DataPath != "" {
			actualConnectInfo = cfg.CSV.DataPath
			log.InfoContextf(ctx, "connectInfo为localhost，使用配置文件路径: %s", actualConnectInfo)

			// 确保目录存在
			if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
				log.ErrorContextf(ctx, "创建CSV数据目录失败: %v", err)
				return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建CSV数据目录失败: %v", err))
			}
		} else {
			// 如果配置不可用，使用默认路径
			actualConnectInfo = "../database/csv"
			log.WarnContextf(ctx, "配置不可用，使用默认CSV路径: %s", actualConnectInfo)

			// 确保目录存在
			if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
				log.ErrorContextf(ctx, "创建CSV数据目录失败: %v", err)
				return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建CSV数据目录失败: %v", err))
			}
		}
	}
	if absPath, err := filepath.Abs(actualConnectInfo); err == nil {
		actualConnectInfo = absPath
	}

	// 保存连接信息（文件路径）
	c.connInfo = actualConnectInfo
	log.InfoContextf(ctx, "CSV连接路径设置成功: %s", actualConnectInfo)
	return nil
}

// GetDeviceKey 获取存储设备名
func (c *CSV) GetDeviceKey() string {
	return "csv"
}

// CloseDeviceConn 关闭CSV设备连接
func (c *CSV) CloseDeviceConn() error {
	// CSV适配器无需关闭连接，每次写入都是独立的文件操作
	return nil
}

// GetFieldInfos 统一获取数据接口
func (c *CSV) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	// CSV适配器不支持查询操作
	return nil, nil
}

// SearchFieldInfos 统一搜索接口
func (c *CSV) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	// CSV适配器不支持搜索操作
	return nil, 0, nil
}

// DeleteRows 统一删除数据接口(CSV适配器不支持删除操作)
func (c *CSV) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
	// CSV适配器不支持删除操作
	return &pb.DeleteRowsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_NOT_SUPPORT,
			Msg:  "CSV适配器不支持删除操作",
		},
		DeletedCount: 0,
	}, nil
}
