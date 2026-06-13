// Package dao 提供数据适配层服务路由接口以及存储相关接口
package dao

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Storer 存储接口
type Storer interface {
	Router
	Executor
	TableOperator
}

// Router 路由接口
type Router interface {
	// GetDeviceTableID 根据逻辑表ID获得底层物理设备表名（上层给到的逻辑表名，各个存储设备可能有不同的处理）
	GetDeviceTableID(logicTableID string) string
	// GetDeviceConn 根据 GetDeviceTableID 生成的存储对象信息进行连接操作
	GetDeviceConn(connectInfo string) error
	// CloseDeviceConn 关闭设备连接
	CloseDeviceConn() error
	// GetDeviceKey 获取存储设备名
	GetDeviceKey() string
}

// Executor 数据存储操作接口，同时处理静态数据和时序数据
type Executor interface {
	// SetFieldInfos 统一更新数据接口(支持静态数据和时序数据的更新)
	SetFieldInfos(ctx context.Context, params *SetFieldParams) (*pb.SetFieldInfosRsp, error)
	// GetFieldInfos 统一获取数据接口(支持静态数据和时序数据的获取)
	GetFieldInfos(ctx context.Context, params *GetFieldParams) ([]*pb.DocRow, error)
	// SearchFieldInfos 统一搜索接口(支持静态数据和时序数据的搜索)
	SearchFieldInfos(ctx context.Context, params *SearchFieldParams) ([]*pb.DocRow, uint64, error)
	// DeleteRows 统一删除数据接口(软删除，设置_deleted字段)
	DeleteRows(ctx context.Context, params *DeleteRowsParams) (*pb.DeleteRowsRsp, error)
}

// TableOperator 表操作接口
type TableOperator interface {
	// CreateTable 创建表
	CreateTable(ctx context.Context, params *CreateTableParams) error
	// DropTable 删除表
	DropTable(ctx context.Context, tableName string) error
	// CheckTable 检查表是否存在
	CheckTable(ctx context.Context, tableName string) (bool, error)
	// GetSchemaFieldLimit 获取指定字段类型的最大数量限制
	GetSchemaFieldLimit(fieldType string) (int, error)
}

// 全局注册机制
var (
	// globalSchemaProvider 全局的 schema 字段限制提供者
	globalSchemaProvider TableOperator
	// schemaProviderMutex 保护全局 schema 提供者的互斥锁
	schemaProviderMutex sync.RWMutex
)

// RegisterSchemaProvider 注册 schema 字段限制提供者
// 通常在存储设备初始化时调用，用于自注册
func RegisterSchemaProvider(provider TableOperator) {
	schemaProviderMutex.Lock()
	defer schemaProviderMutex.Unlock()
	globalSchemaProvider = provider
}

// GetSchemaProvider 获取已注册的 schema 字段限制提供者
func GetSchemaProvider() TableOperator {
	schemaProviderMutex.RLock()
	defer schemaProviderMutex.RUnlock()
	return globalSchemaProvider
}
