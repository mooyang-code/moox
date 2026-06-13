// Package duckdb DuckDB的公共逻辑
// 物理设备层（即DAO层）的抽象是，给上层提供一张物理表的读写接口。
// 根据物理设备的特性，DAO层接口可以选择支持时序/静态数据的读写，普通字段/map字段的读写。也可以全部支持，也可以部分支持。
// 上层logic层，会根据用户配置，请求DAO层接口实现一个逻辑大宽表的视图。
// 这个逻辑大宽表可能会水平切分成N个物理设备表，或纵向切分成普通字段/map字段物理设备表。切分和路由策略在logic层实现。
// 物理设备层只简单处理单表数据读写。
package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// DuckDB DuckDB存储对象
type DuckDB struct {
	// isGetAll 是否获取所有字段
	isGetAll bool
	// db 数据库连接
	db *sql.DB
	// tableID 表名
	tableID string
	// data 当前字段值
	data map[string]any
}

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// GetDeviceTableID 获取物理设备中的物理表名（一般用于分库分表）
func (d *DuckDB) GetDeviceTableID(logicTableID string) string {
	// DuckDB的物理表名，不做特殊处理，直接返回逻辑表名
	return logicTableID
}

// GetDeviceConn 连接DuckDB物理设备
func (d *DuckDB) GetDeviceConn(connectInfo string) error {
	ctx := context.Background()
	log.DebugContextf(ctx, "duckdb connectInfo is %s", connectInfo)

	// 处理connectInfo为localhost的情况，使用配置文件中的路径
	actualConnectInfo := connectInfo
	if connectInfo == "localhost" {
		cfg := config.GetGlobalConfig()
		if cfg != nil && cfg.DuckDB.DataPath != "" {
			// 配置中只有目录路径，需要添加数据库文件名
			actualConnectInfo = filepath.Join(cfg.DuckDB.DataPath, "data.db")
			log.DebugContextf(ctx, "connectInfo为localhost，使用配置文件路径: %s", actualConnectInfo)
		} else {
			// 如果配置不可用，使用默认路径
			actualConnectInfo = "../database/duckdb/data.db"
			log.WarnContextf(ctx, "配置不可用，使用默认DuckDB路径: %s", actualConnectInfo)
		}
	}
	if actualConnectInfo != ":memory:" {
		if absPath, err := filepath.Abs(actualConnectInfo); err == nil {
			actualConnectInfo = absPath
		}
		// 确保目录存在
		dir := filepath.Dir(actualConnectInfo)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.ErrorContextf(ctx, "创建DuckDB数据目录失败: %v", err)
			return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建DuckDB数据目录失败: %v", err))
		}
	}

	// 连接DuckDB数据库
	db, err := sql.Open("duckdb", actualConnectInfo)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to connect to DuckDB: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("Failed to connect to DuckDB: %v", err))
	}
	d.db = db
	// 连接级设置：内存限制与插入顺序策略
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		log.WarnContextf(ctx, "DuckDB配置未加载，跳过连接级参数设置")
	} else if cfg.DuckDB.MemoryLimit == "" {
		log.WarnContextf(ctx, "DuckDB memory_limit为空，跳过设置")
	} else if _, err := d.db.ExecContext(ctx, fmt.Sprintf("SET memory_limit='%s'", cfg.DuckDB.MemoryLimit)); err != nil {
		log.ErrorContextf(ctx, "设置DuckDB memory_limit失败: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("设置DuckDB memory_limit失败: %v", err))
	}

	if _, err := d.db.ExecContext(ctx, "SET preserve_insertion_order=false"); err != nil {
		log.WarnContextf(ctx, "设置DuckDB preserve_insertion_order失败: %v", err)
	}
	log.InfoContextf(ctx, "DuckDB连接成功，实际连接信息: %s", actualConnectInfo)
	return nil
}

// GetDeviceKey 返回DuckDB实例名称或连接标识
func (d *DuckDB) GetDeviceKey() string {
	return "duckdb"
}

// CloseDeviceConn 关闭DuckDB连接
func (d *DuckDB) CloseDeviceConn() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// buildNotDeletedCondition 构建未删除条件
func (d *DuckDB) buildNotDeletedCondition() string {
	// 过滤软删除的数据：_deleted = 0 或 _deleted IS NULL
	return "(_deleted = 0 OR _deleted IS NULL)"
}
