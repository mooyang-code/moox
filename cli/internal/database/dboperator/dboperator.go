package dboperator

import (
	"fmt"

	"github.com/mooyang-code/moox/cli/internal/config"
	"github.com/mooyang-code/moox/cli/internal/database/drivers"
	"github.com/mooyang-code/moox/cli/internal/utils"
)

// DBOperator 数据库操作类
type DBOperator struct {
	Config   *config.Config         // 配置
	DBDriver drivers.DatabaseDriver // 数据库驱动
}

// NewDBOperator 创建新的数据库操作类实例
func NewDBOperator(config *config.Config) (*DBOperator, error) {
	if config == nil {
		return nil, fmt.Errorf("配置参数为空")
	}

	// 创建数据库操作类实例
	operator := &DBOperator{
		Config: config,
	}

	// 从配置中获取存储设备信息
	storageDevice := config.MetadataDatabase.StorageDevice
	if storageDevice == "" {
		return nil, fmt.Errorf("配置中未指定存储设备")
	}

	// 使用工厂方法创建数据库驱动
	dbDriver, err := drivers.CreateDatabaseDriver(storageDevice)
	if err != nil {
		return nil, fmt.Errorf("创建数据库驱动失败: %v", err)
	}
	operator.DBDriver = dbDriver

	return operator, nil
}

// Close 关闭数据库连接
func (op *DBOperator) Close() error {
	if op.DBDriver != nil {
		return op.DBDriver.Close()
	}
	return nil
}

// CreateTablesFromFile 从文件中读取建表语句并创建数据库表
func (op *DBOperator) CreateTablesFromFile(filePath string) error {
	// 读取 SQL 文件
	sqlStatements, err := utils.ReadSQLFromFile(filePath)
	if err != nil {
		return fmt.Errorf("读取 SQL 文件失败: %v", err)
	}

	fmt.Printf("从文件 %s 中读取了 %d 条 SQL 语句\n", filePath, len(sqlStatements))

	// 执行每条 SQL 语句
	for i, stmt := range sqlStatements {
		tableName := utils.ExtractTableName(stmt)
		if tableName == "" {
			fmt.Printf("跳过非建表语句: %s\n", stmt[:min(len(stmt), 50)]+"...")
			continue // 跳过非建表语句
		}

		// 从CREATE TABLE语句中提取表结构
		schema := utils.ExtractTableSchema(stmt)
		if schema == "" {
			fmt.Printf("警告: 无法从SQL语句中提取表结构，将直接执行SQL语句: %s\n", stmt[:min(len(stmt), 50)]+"...")
			// 当无法提取结构时，直接执行SQL语句
			err = op.DBDriver.ExecuteSQL(stmt)
			if err != nil {
				return fmt.Errorf("执行SQL语句失败 (语句 %d): %v", i+1, err)
			}
			continue
		}

		// 使用CreateTable函数创建表
		fmt.Printf("正在创建表 %s...\n", tableName)
		err = op.DBDriver.CreateTable(tableName, schema)
		if err != nil {
			return fmt.Errorf("创建表 %s 失败: %v", tableName, err)
		}
	}
	return nil
}

// min 返回两个int中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// InsertDataFromYAMLFile 从YAML文件读取数据并插入到数据库表中
func (op *DBOperator) InsertDataFromYAMLFile(filePath string) error {
	// 读取YAML文件
	yamlData, err := utils.ReadYAMLFromFile(filePath)
	if err != nil {
		return fmt.Errorf("读取YAML文件失败: %v", err)
	}

	// 处理每个表的数据
	for tableName, tableData := range yamlData {
		// 检查表是否存在
		exists, err := op.DBDriver.TableExists(tableName)
		if err != nil {
			return fmt.Errorf("检查表 %s 是否存在失败: %v", tableName, err)
		}

		if !exists {
			return fmt.Errorf("表 %s 不存在，请先创建表", tableName)
		}

		// 解析表数据
		records, ok := tableData.([]any)
		if !ok {
			return fmt.Errorf("表 %s 的数据格式不正确，应为记录数组", tableName)
		}

		// 处理每条记录
		for i, record := range records {
			recordMap, ok := record.(map[string]any)
			if !ok {
				return fmt.Errorf("表 %s 的第 %d 条记录格式不正确", tableName, i+1)
			}

			// 使用驱动层的InsertData方法插入数据
			err = op.DBDriver.InsertData(tableName, recordMap)
			if err != nil {
				return fmt.Errorf("插入数据失败: %v", err)
			}
		}

		fmt.Printf("成功向表 %s 插入 %d 条记录\n", tableName, len(records))
	}
	return nil
}

// CreateMetaTable 创建元数据表
func (op *DBOperator) CreateMetaTable(dataFile string) error {
	fmt.Printf("使用文件 %s 创建元数据表...\n", dataFile)

	// 从文件执行建表语句
	err := op.CreateTablesFromFile(dataFile)
	if err != nil {
		return fmt.Errorf("执行 SQL 文件失败: %v", err)
	}
	fmt.Println("元数据表创建成功！")
	return nil
}

// CreateTable 根据配置信息新建数据表
func (op *DBOperator) CreateTable(tableName string) error {
	fmt.Printf("根据配置信息创建数据表 %s...\n", tableName)
	// TODO: 实现具体逻辑
	return nil
}

// InsertDataFromFile 从 YAML 文件中读取数据并插入表
func (op *DBOperator) InsertDataFromFile(dataFile string) error {
	fmt.Printf("从文件 %s 中读取数据并插入表...\n", dataFile)

	// 从YAML文件插入数据
	err := op.InsertDataFromYAMLFile(dataFile)
	if err != nil {
		return fmt.Errorf("插入数据失败: %v", err)
	}
	fmt.Println("数据插入成功！")
	return nil
}

// ShowSchema 查看表结构
func (op *DBOperator) ShowSchema(tableName string) error {
	fmt.Printf("查看表 %s 的结构...\n", tableName)

	// 检查表是否存在
	exists, err := op.DBDriver.TableExists(tableName)
	if err != nil {
		return fmt.Errorf("检查表 %s 是否存在失败: %v", tableName, err)
	}
	if !exists {
		return fmt.Errorf("表 %s 不存在", tableName)
	}

	// 调用驱动层的ShowTableSchema方法显示表结构
	err = op.DBDriver.ShowTableSchema(tableName)
	if err != nil {
		return fmt.Errorf("获取表 %s 结构失败: %v", tableName, err)
	}
	return nil
}

// ShowData 查看表的最近数据
func (op *DBOperator) ShowData(tableName string) error {
	fmt.Printf("查看表 %s 的最近数据...\n", tableName)

	// 检查表是否存在
	exists, err := op.DBDriver.TableExists(tableName)
	if err != nil {
		return fmt.Errorf("检查表 %s 是否存在失败: %v", tableName, err)
	}
	if !exists {
		return fmt.Errorf("表 %s 不存在", tableName)
	}

	// 显示最近的10条数据
	const limit = 10
	err = op.DBDriver.ShowData(tableName, limit)
	if err != nil {
		return fmt.Errorf("获取表 %s 数据失败: %v", tableName, err)
	}
	return nil
}
