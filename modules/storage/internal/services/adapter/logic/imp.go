package logic

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/bleve"  // 注册Bleve设备(触发init函数)
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/csv"    // 注册CSV设备(触发init函数)
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/duckdb" // 注册DuckDB设备(触发init函数)
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao/pebble" // 注册Pebble设备(触发init函数)
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// AdapterImpl 适配器实现结构体
type AdapterImpl struct {
	cfg *config.Config
}

// InitAdapterImpl 新建存储适配层服务实现
func InitAdapterImpl(adapterCfg *config.Config) (*AdapterImpl, error) {
	// 初始化缓存组件
	if err := cache.InitSingleDBCacheWithPollingInterval(
		time.Duration(adapterCfg.SchemaCachePollingIntervalSeconds)*time.Second,
		// 字段信息表
		cache.Field{
			AccessUrl: adapterCfg.SchemaCaches[cache.Field{}.SchemaID()],
		},
		// 字段路由表
		cache.FieldRoute{
			AccessUrl: adapterCfg.SchemaCaches[cache.FieldRoute{}.SchemaID()],
		},
		// 存储设备表
		cache.StorageDevice{
			AccessUrl: adapterCfg.SchemaCaches[cache.StorageDevice{}.SchemaID()],
		},
		// 字段列名映射表
		cache.FieldColumnMap{
			AccessUrl: adapterCfg.SchemaCaches[cache.FieldColumnMap{}.SchemaID()],
		},
	); err != nil {
		log.Fatalf("InitSingleDBCache err[%v]", err)
	}
	return &AdapterImpl{cfg: adapterCfg}, nil
}

// DeleteRows 统一删除数据接口(软删除，设置_deleted字段)
func (a *AdapterImpl) DeleteRows(ctx context.Context, req *pb.DeleteRowsReq) (*pb.DeleteRowsRsp, error) {
	log.DebugContextf(ctx, "DeleteRows: req=%v", req)
	return a.deleteRows(ctx, req)
}
