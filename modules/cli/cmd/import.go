package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	"github.com/mooyang-code/moox/modules/cli/internal/utils"
	"github.com/spf13/cobra"
)

// ImportOperator 数据导入操作器
type ImportOperator struct {
	Config *config.Config
	DB     *sql.DB
}

// 导入命令标志
var (
	importFile  string
	importTable string
	clearTable  bool
)

// importCmd represents the import command
var importCmd = &cobra.Command{
	Use:   "import",
	Short: "数据导入命令",
	Long:  "从 YAML 文件中读取表信息并插入到目标数据库表中。支持批量数据导入和事务安全。\n\n示例:\n  ./moox-cli import --file config/collector.yaml\n  ./moox-cli import -f data.yaml --clear\n  ./moox-cli import -f data.yaml -t specific_table",
	Run: func(cmd *cobra.Command, args []string) {
		// 验证必要参数
		if importFile == "" {
			fmt.Printf("%s错误: 请指定 YAML 文件路径 (使用 --file 参数)%s\n", ColorRed, ColorReset)
			cmd.Help()
			return
		}

		// 执行数据导入
		if err := runImport(); err != nil {
			fmt.Printf("%s导入失败: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
	},
}

// runImport 执行数据导入
func runImport() error {
	fmt.Printf("%s=== MooX 数据导入工具 ===%s\n\n", ColorCyan, ColorReset)

	// 创建导入操作器
	importer, err := NewImportOperator(AppConfig)
	if err != nil {
		return fmt.Errorf("创建导入操作器失败: %v", err)
	}
	defer importer.Close()

	// 执行导入
	return importer.ImportFromYAML(importFile, importTable, clearTable)
}

// NewImportOperator 创建新的导入操作器
func NewImportOperator(config *config.Config) (*ImportOperator, error) {
	if config == nil {
		return nil, fmt.Errorf("配置参数为空")
	}

	// 获取数据库连接
	storageDevice := config.MetadataDatabase.StorageDevice
	if storageDevice == "" {
		return nil, fmt.Errorf("配置中未指定存储设备")
	}

	// 创建数据库连接字符串，使用默认路径
	dbPath := "../data/moox.db" // 默认路径

	// 连接数据库
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %v", err)
	}

	// 测试连接
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("数据库连接测试失败: %v", err)
	}

	return &ImportOperator{
		Config: config,
		DB:     db,
	}, nil
}

// ImportFromYAML 从YAML文件导入数据
func (importer *ImportOperator) ImportFromYAML(filePath, targetTable string, clearBeforeImport bool) error {
	// 读取YAML文件
	fmt.Printf("%s📁 正在读取 YAML 文件: %s%s\n", ColorBlue, filePath, ColorReset)
	yamlData, err := utils.ReadYAMLFromFile(filePath)
	if err != nil {
		return fmt.Errorf("读取YAML文件失败: %v", err)
	}

	fmt.Printf("%s✓ YAML 文件读取成功，包含 %d 个表%s\n", ColorGreen, len(yamlData), ColorReset)

	// 开始事务
	fmt.Printf("%s🔄 开始数据库事务...%s\n", ColorBlue, ColorReset)
	tx, err := importer.DB.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %v", err)
	}
	defer tx.Rollback()

	// 处理每个表的数据
	for tableName, tableData := range yamlData {
		actualTableName := tableName
		if targetTable != "" {
			actualTableName = targetTable
		}

		if err := importer.importTableData(tx, actualTableName, tableData, clearBeforeImport); err != nil {
			return fmt.Errorf("导入表 %s 失败: %v", actualTableName, err)
		}
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %v", err)
	}

	fmt.Printf("%s🎉 数据导入完成！%s\n", ColorGreen, ColorReset)
	return nil
}

// importTableData 导入单个表的数据
func (importer *ImportOperator) importTableData(tx *sql.Tx, tableName string, tableData any, clearBeforeImport bool) error {
	// 检查表是否存在
	exists, err := importer.checkTableExists(tx, tableName)
	if err != nil {
		return fmt.Errorf("检查表 %s 是否存在失败: %v", tableName, err)
	}

	if !exists {
		return fmt.Errorf("表 %s 不存在，请先创建表", tableName)
	}

	// 如果需要清空表数据
	if clearBeforeImport {
		fmt.Printf("%s🗑️  清空表 %s 数据...%s\n", ColorYellow, tableName, ColorReset)
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s", tableName)); err != nil {
			return fmt.Errorf("清空表 %s 失败: %v", tableName, err)
		}
	}

	// 解析表数据
	records, ok := tableData.([]any)
	if !ok {
		return fmt.Errorf("表 %s 的数据格式不正确，应为记录数组", tableName)
	}

	fmt.Printf("%s📊 正在导入表 %s，共 %d 条记录...%s\n", ColorBlue, tableName, len(records), ColorReset)

	// 插入数据
	successCount := 0
	failedRecords := make([]int, 0)

	for i, record := range records {
		recordMap, ok := record.(map[string]any)
		if !ok {
			failedRecords = append(failedRecords, i+1)
			continue
		}

		// 构建插入SQL
		insertSQL, params, err := importer.buildInsertSQL(tableName, recordMap)
		if err != nil {
			failedRecords = append(failedRecords, i+1)
			continue
		}

		// 执行插入
		if _, err := tx.Exec(insertSQL, params...); err != nil {
			// 如果是主键冲突，尝试更新
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
				updateSQL, updateParams, updateErr := importer.buildUpdateSQL(tableName, recordMap)
				if updateErr == nil {
					if _, updateErr := tx.Exec(updateSQL, updateParams...); updateErr == nil {
						successCount++
						continue
					}
				}
			}
			failedRecords = append(failedRecords, i+1)
		} else {
			successCount++
		}
	}

	// 显示导入结果
	fmt.Printf("%s✓ 表 %s 导入完成: 成功 %d 条", ColorGreen, tableName, successCount)
	if len(failedRecords) > 0 {
		fmt.Printf(", 失败 %d 条 (记录: %v)", len(failedRecords), failedRecords)
	}
	fmt.Println(ColorReset)

	return nil
}

// checkTableExists 检查表是否存在
func (importer *ImportOperator) checkTableExists(tx *sql.Tx, tableName string) (bool, error) {
	var count int
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?"
	err := tx.QueryRow(query, tableName).Scan(&count)
	return count > 0, err
}

// buildInsertSQL 构建插入SQL语句
func (importer *ImportOperator) buildInsertSQL(tableName string, record map[string]any) (string, []any, error) {
	if len(record) == 0 {
		return "", nil, fmt.Errorf("记录为空")
	}

	// 构建列名和占位符
	columns := make([]string, 0, len(record))
	placeholders := make([]string, 0, len(record))
	values := make([]any, 0, len(record))

	for column, value := range record {
		columns = append(columns, column)
		placeholders = append(placeholders, "?")
		values = append(values, value)
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tableName,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	return sql, values, nil
}

// buildUpdateSQL 构建更新SQL语句（用于主键冲突时）
func (importer *ImportOperator) buildUpdateSQL(tableName string, record map[string]any) (string, []any, error) {
	if len(record) == 0 {
		return "", nil, fmt.Errorf("记录为空")
	}

	// 假设第一列是主键
	updateColumns := make([]string, 0, len(record)-1)
	updateValues := make([]any, 0, len(record)-1)

	// 获取列和值
	firstColumn := ""
	firstValue := any(nil)

	for column, value := range record {
		if firstColumn == "" {
			firstColumn = column
			firstValue = value
		} else {
			updateColumns = append(updateColumns, fmt.Sprintf("%s=?", column))
			updateValues = append(updateValues, value)
		}
	}

	if firstColumn == "" {
		return "", nil, fmt.Errorf("无法确定主键列")
	}

	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s=?", tableName,
		strings.Join(updateColumns, ", "),
		firstColumn)

	// 组合参数：更新值 + 主键值
	params := append(updateValues, firstValue)

	return sql, params, nil
}

// Close 关闭数据库连接
func (importer *ImportOperator) Close() error {
	if importer.DB != nil {
		return importer.DB.Close()
	}
	return nil
}

func init() {
	rootCmd.AddCommand(importCmd)

	// 添加导入命令标志
	importCmd.Flags().StringVarP(&importFile, "file", "f", "", "YAML文件路径 (必填)")
	importCmd.Flags().StringVarP(&importTable, "table", "t", "", "目标表名 (可选，默认为yaml中的表名)")
	importCmd.Flags().BoolVarP(&clearTable, "clear", "c", false, "导入前清空表数据")

	// 设置标志为必填
	importCmd.MarkFlagRequired("file")
}
