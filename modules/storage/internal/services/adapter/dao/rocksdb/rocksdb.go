//go:build !norocksdb && cgo
// +build !norocksdb,cgo

// Package rocksdb RocksDB的公共逻辑
// 物理设备层（即DAO层）的抽象是，给上层提供一张物理表的读写接口。
// 根据物理设备的特性，DAO层接口可以选择支持时序/静态数据的读写，普通字段/map字段的读写。也可以全部支持，也可以部分支持。
// 上层logic层，会根据用户配置，请求DAO层接口实现一个逻辑大宽表的视图。
// 这个逻辑大宽表可能会水平切分成N个物理设备表，或纵向切分成普通字段/map字段物理设备表。切分和路由策略在logic层实现。
// 物理设备层只简单处理单表数据读写。
package rocksdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/linxGnu/grocksdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// RocksDB RocksDB存储对象
type RocksDB struct {
	// db 数据库实例
	db *grocksdb.DB
	// tableID 当前操作的表ID
	tableID string
	// wo 写选项
	wo *grocksdb.WriteOptions
	// ro 读选项
	ro *grocksdb.ReadOptions
	// actualConnPath 实际使用的连接路径（用于连接池识别）
	actualConnPath string
}

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// GetDeviceTableID 获取物理设备中的物理表名（一般用于分库分表）
func (r *RocksDB) GetDeviceTableID(logicTableID string) string {
	// RocksDB的物理表名，不做特殊处理，直接返回逻辑表名
	return logicTableID
}

// GetDeviceConn 连接RocksDB物理设备
func (r *RocksDB) GetDeviceConn(connectInfo string) error {
	ctx := context.Background()
	log.DebugContextf(ctx, "rocksdb connectInfo is %s", connectInfo)

	// 处理connectInfo为localhost的情况，使用配置文件中的路径
	actualConnectInfo := connectInfo
	if connectInfo == "localhost" {
		cfg := config.GetGlobalConfig()
		if cfg != nil && cfg.RocksDB.DataPath != "" {
			actualConnectInfo = cfg.RocksDB.DataPath
			log.DebugContextf(ctx, "connectInfo为localhost，使用配置文件路径: %s", actualConnectInfo)
		} else {
			// 如果配置不可用，使用默认路径
			actualConnectInfo = "../database/rocksdb"
			log.WarnContextf(ctx, "配置不可用，使用默认RocksDB路径: %s", actualConnectInfo)
		}
	}
	if absPath, err := filepath.Abs(actualConnectInfo); err == nil {
		actualConnectInfo = absPath
	}

	// 确保目录存在
	if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
		log.ErrorContextf(ctx, "创建RocksDB数据目录失败: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建RocksDB数据目录失败: %v", err))
	}

	// 配置 RocksDB Options
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	opts.SetCompression(grocksdb.SnappyCompression)

	// 设置缓存
	cfg := config.GetGlobalConfig()
	blockCacheMB := int64(512) // 默认 512MB
	if cfg != nil && cfg.RocksDB.BlockCacheMB > 0 {
		blockCacheMB = cfg.RocksDB.BlockCacheMB
	}
	blockCache := grocksdb.NewLRUCache(uint64(blockCacheMB * 1024 * 1024))
	opts.SetBlockBasedTableFactory(grocksdb.NewDefaultBlockBasedTableOptions())
	bbto := grocksdb.NewDefaultBlockBasedTableOptions()
	bbto.SetBlockCache(blockCache)
	opts.SetBlockBasedTableFactory(bbto)

	// 设置 Bloom Filter
	bbto.SetFilterPolicy(grocksdb.NewBloomFilter(10))

	// 打开数据库
	db, err := grocksdb.OpenDb(opts, actualConnectInfo)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to connect to RocksDB: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("Failed to connect to RocksDB: %v", err))
	}

	r.db = db
	r.wo = grocksdb.NewDefaultWriteOptions()
	r.ro = grocksdb.NewDefaultReadOptions()
	r.actualConnPath = actualConnectInfo // 保存实际连接路径

	log.InfoContextf(ctx, "RocksDB连接成功，实际连接信息: %s", actualConnectInfo)
	return nil
}

// GetDeviceKey 返回RocksDB实例名称或连接标识
func (r *RocksDB) GetDeviceKey() string {
	return "rocksdb"
}

// GetActualConnPath 返回实际使用的连接路径
func (r *RocksDB) GetActualConnPath() string {
	return r.actualConnPath
}

// CloseDeviceConn 关闭RocksDB连接
func (r *RocksDB) CloseDeviceConn() error {
	if r.wo != nil {
		r.wo.Destroy()
		r.wo = nil
	}
	if r.ro != nil {
		r.ro.Destroy()
		r.ro = nil
	}
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}
