package bleve

import (
	"context"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// CreateTable 创建表
func (b *Bleve) CreateTable(ctx context.Context, params *dao.CreateTableParams) error {
	if params == nil {
		return fmt.Errorf("创建表参数不能为空")
	}
	if params.TableID == "" {
		return fmt.Errorf("表名不能为空")
	}

	log.InfoContextf(ctx, "开始创建Bleve表: %s", params.TableID)

	// TODO: 实现具体的创建表逻辑
	// 当前为空实现，后续可以根据需要添加具体逻辑

	log.InfoContextf(ctx, "表[%s]创建成功（空实现）", params.TableID)
	return nil
}

// CheckTable 检查表是否存在
func (b *Bleve) CheckTable(ctx context.Context, tableName string) (bool, error) {
	if tableName == "" {
		return false, fmt.Errorf("表名不能为空")
	}

	// TODO: 实现具体的检查表逻辑
	// 当前为空实现，后续可以根据需要添加具体逻辑

	log.DebugContextf(ctx, "表[%s]存在性检查结果: false（空实现）", tableName)
	return false, nil
}

// DropTable 删除表
func (b *Bleve) DropTable(ctx context.Context, tableName string) error {
	if tableName == "" {
		return fmt.Errorf("表名不能为空")
	}

	// 构建表对应的索引路径
	indexPath := b.getTableIndexPath(tableName)

	// 使用os.RemoveAll删除索引目录
	if err := os.RemoveAll(indexPath); err != nil {
		log.ErrorContextf(ctx, "删除表[%s]失败: %v", tableName, err)
		return err
	}

	log.InfoContextf(ctx, "表[%s]删除成功", tableName)
	return nil
}

// GetSchemaFieldLimit 获取指定字段类型的最大数量限制
// Bleve 存储的默认实现，返回固定的限制值
func (b *Bleve) GetSchemaFieldLimit(fieldType string) (int, error) {
	return 0, nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// isTableExists 检查表是否存在
func (b *Bleve) isTableExists(tableName string) bool {
	indexPath := b.getTableIndexPath(tableName)
	_, err := os.Stat(indexPath)
	return err == nil
}

// ensureIndexDirectory 确保索引目录存在
func (b *Bleve) ensureIndexDirectory() error {
	// 创建基础索引目录
	if err := os.MkdirAll(b.indexPath, 0755); err != nil {
		return fmt.Errorf("failed to create index directory: %v", err)
	}
	return nil
}
