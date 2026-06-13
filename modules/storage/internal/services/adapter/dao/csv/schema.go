package csv

import (
	"context"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
)

// CreateTable 创建表
func (c *CSV) CreateTable(ctx context.Context, params *dao.CreateTableParams) error {
	if params == nil {
		return fmt.Errorf("创建表参数不能为空")
	}
	if params.TableID == "" {
		return fmt.Errorf("表名不能为空")
	}

	// TODO: 实现具体的创建表逻辑
	// 当前为空实现，后续可以根据需要添加具体逻辑

	return nil
}

// CheckTable 检查表是否存在
func (c *CSV) CheckTable(ctx context.Context, tableName string) (bool, error) {
	if tableName == "" {
		return false, fmt.Errorf("表名不能为空")
	}

	// TODO: 实现具体的检查表逻辑
	// 当前为空实现，后续可以根据需要添加具体逻辑

	return false, nil
}

// DropTable 删除表
func (c *CSV) DropTable(ctx context.Context, tableName string) error {
	// 生成CSV文件名
	filename := c.getCSVFilename(tableName)

	// 删除文件
	if err := os.Remove(filename); err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在视为成功
		}
		return fmt.Errorf("failed to drop table: %v", err)
	}
	return nil
}

// GetSchemaFieldLimit 获取指定字段类型的最大数量限制
// CSV 存储的默认实现，返回固定的限制值
func (c *CSV) GetSchemaFieldLimit(fieldType string) (int, error) {
	return 0, nil
}
