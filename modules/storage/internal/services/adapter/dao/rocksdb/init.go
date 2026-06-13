//go:build !norocksdb && cgo
// +build !norocksdb,cgo

// Package rocksdb RocksDB相关逻辑
package rocksdb

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// ============================================================================
// 初始化函数 - 包初始化和构造函数
// ============================================================================

// init 包初始化函数，将 RocksDB 注册到设备系统
func init() {
	// 注册 RocksDB 设备类型
	dao.RegisterDeviceType(pb.EnumDeviceType_ROCKDB_DEVICE, func(ctx context.Context) (dao.Storer, error) {
		return NewRocksDB(ctx), nil
	})
}

// NewRocksDB 创建新的RocksDB存储对象
func NewRocksDB(ctx context.Context) *RocksDB {
	return &RocksDB{}
}
