//go:build !noduckdb && cgo
// +build !noduckdb,cgo

// Package duckdb DuckDB相关逻辑
package duckdb

import (
	"context"

	_ "github.com/marcboeker/go-duckdb/v2" // 导入 DuckDB 驱动
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// ============================================================================
// 初始化函数 - 包初始化和构造函数
// ============================================================================

// init 包初始化函数，将 DuckDB 注册到设备系统
func init() {
	// 注册 DuckDB 设备类型
	dao.RegisterDeviceType(pb.EnumDeviceType_DUCKDB_DEVICE, func(ctx context.Context) (dao.Storer, error) {
		return NewDuckDB(ctx), nil
	})

	// 注册 DuckDB 为全局的 schema 字段限制提供者
	// 创建一个 DuckDB 实例用于提供 schema 信息（不需要数据库连接）
	schemaProvider := &DuckDB{}
	dao.RegisterSchemaProvider(schemaProvider)
}

// NewDuckDB 创建新的DuckDB存储对象
func NewDuckDB(ctx context.Context) *DuckDB {
	return &DuckDB{
		isGetAll: false,
		data:     make(map[string]any),
	}
}
