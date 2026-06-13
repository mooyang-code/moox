//go:build !norocksdb && cgo
// +build !norocksdb,cgo

package rocksdb

import (
	"context"
	"fmt"
	"sync"

	"github.com/linxGnu/grocksdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// 表级锁，防止并发创建/删除表
var createTableLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{
	locks: make(map[string]*sync.Mutex),
}

func lockCreateTable(tableName string) func() {
	createTableLocks.mu.Lock()
	lock, ok := createTableLocks.locks[tableName]
	if !ok {
		lock = &sync.Mutex{}
		createTableLocks.locks[tableName] = lock
	}
	createTableLocks.mu.Unlock()

	lock.Lock()
	return func() {
		lock.Unlock()
	}
}

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// CreateTable 创建表
func (r *RocksDB) CreateTable(ctx context.Context, params *dao.CreateTableParams) error {
	if params == nil {
		return fmt.Errorf("创建表参数不能为空")
	}
	if params.TableID == "" {
		return fmt.Errorf("表名不能为空")
	}

	tableID := params.TableID
	unlock := lockCreateTable(tableID)
	defer unlock()

	log.DebugContextf(ctx, "开始创建RocksDB表: %s", tableID)

	// 检查表是否已存在
	exists, err := r.CheckTable(ctx, tableID)
	if err != nil {
		log.ErrorContextf(ctx, "检查表[%s]是否存在失败: %v", tableID, err)
		return err
	}

	// 如果表已存在且不强制创建
	if exists && !params.ForceCreate {
		log.InfoContextf(ctx, "表[%s]已存在，如需覆盖请设置ForceCreate=true", tableID)
		return nil
	}

	// 如果表已存在且强制创建，先删除表
	if exists && params.ForceCreate {
		log.InfoContextf(ctx, "表[%s]已存在，强制创建模式，先删除原表", tableID)
		if err := r.DropTable(ctx, tableID); err != nil {
			log.ErrorContextf(ctx, "删除已存在表[%s]失败: %v", tableID, err)
			return err
		}
	}

	// 创建表元数据标记
	metaKey := buildTableMetaKey(tableID)
	err = r.db.Put(r.wo, []byte(metaKey), []byte("1"))
	if err != nil {
		log.ErrorContextf(ctx, "创建表失败: %v, metaKey: %s", err, metaKey)
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("创建表失败: %v", err))
	}

	log.InfoContextf(ctx, "表[%s]创建成功", tableID)
	return nil
}

// CheckTable 检查表是否存在
func (r *RocksDB) CheckTable(ctx context.Context, tableName string) (bool, error) {
	if tableName == "" {
		return false, fmt.Errorf("表名不能为空")
	}

	metaKey := buildTableMetaKey(tableName)
	value, err := r.db.Get(r.ro, []byte(metaKey))
	if err != nil {
		log.ErrorContextf(ctx, "检查表[%s]是否存在失败: %v", tableName, err)
		return false, err
	}
	defer value.Free()

	exists := value.Exists() && string(value.Data()) == "1"
	log.DebugContextf(ctx, "表[%s]存在性检查结果: %v", tableName, exists)
	return exists, nil
}

// DropTable 删除表
func (r *RocksDB) DropTable(ctx context.Context, tableName string) error {
	if tableName == "" {
		return fmt.Errorf("表名不能为空")
	}

	log.DebugContextf(ctx, "开始删除表: %s", tableName)

	// 扫描表前缀
	tablePrefix := fmt.Sprintf("%s|", tableName)
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	// 收集所有 Key
	var keysToDelete [][]byte
	for it.Seek([]byte(tablePrefix)); it.ValidForPrefix([]byte(tablePrefix)); it.Next() {
		key := make([]byte, len(it.Key().Data()))
		copy(key, it.Key().Data())
		keysToDelete = append(keysToDelete, key)
	}

	if err := it.Err(); err != nil {
		log.ErrorContextf(ctx, "扫描表[%s]数据失败: %v", tableName, err)
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("扫描表数据失败: %v", err))
	}

	// 批量删除
	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	for _, key := range keysToDelete {
		batch.Delete(key)
	}

	// 删除表元数据
	metaKey := buildTableMetaKey(tableName)
	batch.Delete([]byte(metaKey))

	// 提交批量删除
	err := r.db.Write(r.wo, batch)
	if err != nil {
		log.ErrorContextf(ctx, "删除表[%s]失败: %v", tableName, err)
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("删除表失败: %v", err))
	}

	log.InfoContextf(ctx, "表[%s]删除成功，共删除 %d 个Key", tableName, len(keysToDelete)+1)
	return nil
}

// GetSchemaFieldLimit 获取指定字段类型的最大数量限制
// RocksDB 是 KV 存储，理论上字段数量无限制
func (r *RocksDB) GetSchemaFieldLimit(fieldType string) (int, error) {
	// RocksDB 是 KV 存储，理论上字段数量无限制
	// 返回一个足够大的值
	return 100000, nil
}
