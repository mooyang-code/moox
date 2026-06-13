package pebble

import (
	"context"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

func (p *Pebble) CreateTable(ctx context.Context, params *dao.CreateTableParams) error {
	if params == nil {
		return fmt.Errorf("创建表参数不能为空")
	}
	if params.TableID == "" {
		return fmt.Errorf("表名不能为空")
	}

	unlock := lockCreateTable(params.TableID)
	defer unlock()

	exists, err := p.CheckTable(ctx, params.TableID)
	if err != nil {
		return err
	}
	if exists && !params.ForceCreate {
		return nil
	}
	if exists {
		if err := p.DropTable(ctx, params.TableID); err != nil {
			return err
		}
	}
	if err := p.db.Set([]byte(buildTableMetaKey(params.TableID)), []byte("1"), p.writeOptions); err != nil {
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("创建表失败: %v", err))
	}
	return nil
}

func (p *Pebble) CheckTable(ctx context.Context, tableName string) (bool, error) {
	if tableName == "" {
		return false, fmt.Errorf("表名不能为空")
	}
	data, exists, err := p.get(buildTableMetaKey(tableName))
	if err != nil {
		log.ErrorContextf(ctx, "检查表[%s]是否存在失败: %v", tableName, err)
		return false, err
	}
	return exists && string(data) == "1", nil
}

func (p *Pebble) DropTable(ctx context.Context, tableName string) error {
	if tableName == "" {
		return fmt.Errorf("表名不能为空")
	}

	keys, err := p.scanKeysWithPrefix(tableName + "|")
	if err != nil {
		log.ErrorContextf(ctx, "扫描表[%s]数据失败: %v", tableName, err)
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("扫描表数据失败: %v", err))
	}

	batch := p.db.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := batch.Delete(key, nil); err != nil {
			return err
		}
	}
	if err := batch.Delete([]byte(buildTableMetaKey(tableName)), nil); err != nil {
		return err
	}
	if err := batch.Commit(p.writeOptions); err != nil {
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("删除表失败: %v", err))
	}
	return nil
}

func (p *Pebble) GetSchemaFieldLimit(_ string) (int, error) {
	return 100000, nil
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
