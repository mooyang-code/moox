package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/cli/internal/database/dboperator"
	"github.com/mooyang-code/moox/cli/internal/utils"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // SQLite 驱动
)

var (
	metaSchemaFile  string
	createDataTable string
	insertDataFile  string
	showSchemaTable string
	showDataTable   string
	initDB          bool
	dropDB          bool
	dbPath          string
	schemaFile      string
)

var dbCmd = &cobra.Command{
	Use:     "db",
	Aliases: []string{"database"},
	Short:   "数据库操作命令",
	Long:    "提供对数据库的操作，包括初始化数据库、删除数据库、创建元数据表、插入数据、查看表结构、查看表数据等功能。",
	Run: func(cmd *cobra.Command, args []string) {
		// 处理 init 和 drop 操作
		if initDB || dropDB {
			handleDatabaseInitDrop()
			return
		}

		// 创建数据库操作类实例
		dbOp, err := dboperator.NewDBOperator(AppConfig)
		if err != nil {
			fmt.Printf("初始化数据库操作失败: %v\n", err)
			return
		}
		defer dbOp.Close()

		if metaSchemaFile != "" {
			err := dbOp.CreateMetaTable(metaSchemaFile)
			if err != nil {
				fmt.Printf("创建元数据表失败: %v\n", err)
				return
			}

			if insertDataFile != "" {
				err := dbOp.InsertDataFromFile(insertDataFile)
				if err != nil {
					fmt.Printf("插入数据失败: %v\n", err)
				}
			}
		} else if createDataTable != "" {
			err := dbOp.CreateTable(createDataTable)
			if err != nil {
				fmt.Printf("创建数据表失败: %v\n", err)
			}
		} else if insertDataFile != "" {
			err := dbOp.InsertDataFromFile(insertDataFile)
			if err != nil {
				fmt.Printf("插入数据失败: %v\n", err)
			}
		} else if showSchemaTable != "" {
			err := dbOp.ShowSchema(showSchemaTable)
			if err != nil {
				fmt.Printf("查看表结构失败: %v\n", err)
			}
		} else if showDataTable != "" {
			err := dbOp.ShowData(showDataTable)
			if err != nil {
				fmt.Printf("查看表数据失败: %v\n", err)
			}
		} else {
			fmt.Println("请指定操作，例如 --init、--drop、--meta-schema、--create-table、--insert-data、--show-schema 或 --show-data")
		}
	},
}

func init() {
	rootCmd.AddCommand(dbCmd)

	// 初始化和删除数据库相关标志
	dbCmd.Flags().BoolVar(&initDB, "init", false, "初始化数据库")
	dbCmd.Flags().BoolVar(&dropDB, "drop", false, "删除数据库文件（危险操作）")
	dbCmd.Flags().StringVar(&dbPath, "db", "../data/moox.db", "数据库文件路径")
	dbCmd.Flags().StringVar(&schemaFile, "schema", "../sql/schema.sql", "SQL schema 文件路径")

	// 原有的数据库操作标志
	dbCmd.Flags().StringVar(&metaSchemaFile, "meta-schema", "", "使用SQL文件创建元数据表")
	dbCmd.Flags().StringVar(&createDataTable, "create-table", "", "根据配置信息新建数据表")
	dbCmd.Flags().StringVar(&insertDataFile, "insert-data", "", "向表中插入数据（仅支持 YAML 文件）")
	dbCmd.Flags().StringVar(&showSchemaTable, "show-schema", "", "查看表结构")
	dbCmd.Flags().StringVar(&showDataTable, "show-data", "", "查看表的最近数据")

}

// 颜色常量定义
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
)

// handleDatabaseInitDrop 处理数据库初始化和删除操作
func handleDatabaseInitDrop() {
	fmt.Printf("%s=== MooX 数据库管理工具 ===%s\n\n", ColorCyan, ColorReset)

	// 删除数据库操作
	if dropDB {
		if err := dropDatabase(dbPath); err != nil {
			fmt.Printf("%s错误: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
		return
	}

	// 初始化数据库操作
	if initDB {
		if err := initDatabase(dbPath, schemaFile); err != nil {
			fmt.Printf("%s错误: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
		return
	}
}

// dropDatabase 删除数据库文件
func dropDatabase(dbPath string) error {
	fmt.Printf("%s正在删除数据库文件: %s%s\n", ColorYellow, dbPath, ColorReset)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Printf("%s数据库文件不存在，无需删除%s\n", ColorGreen, ColorReset)
		return nil
	}

	if err := os.Remove(dbPath); err != nil {
		return fmt.Errorf("删除数据库文件失败: %v", err)
	}

	fmt.Printf("%s✓ 数据库文件删除成功%s\n", ColorGreen, ColorReset)
	return nil
}

// initDatabase 初始化数据库
func initDatabase(dbPath, schemaFile string) error {
	fmt.Printf("%s正在初始化数据库: %s%s\n", ColorBlue, dbPath, ColorReset)

	// 确保数据目录存在
	dataDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %v", err)
	}
	fmt.Printf("%s✓ 数据目录已准备: %s%s\n", ColorGreen, dataDir, ColorReset)

	// 直接使用 schemaFile 作为路径
	schemaPath := schemaFile

	// 读取 schema 文件
	statements, err := utils.ReadSQLFromFile(schemaPath)
	if err != nil {
		return fmt.Errorf("读取 schema 文件失败: %v", err)
	}
	fmt.Printf("%s✓ 已读取 SQL schema 文件: %s (共 %d 条语句)%s\n", ColorGreen, schemaPath, len(statements), ColorReset)

	// 连接数据库 - 使用 database/sql
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %v", err)
	}
	defer db.Close()

	// 测试连接
	if err := db.Ping(); err != nil {
		return fmt.Errorf("数据库连接测试失败: %v", err)
	}
	fmt.Printf("%s✓ 数据库连接成功%s\n", ColorGreen, ColorReset)

	// 执行 SQL 语句
	if err := executeSQLStatements(db, statements); err != nil {
		return fmt.Errorf("执行 SQL 语句失败: %v", err)
	}

	fmt.Printf("%s🎉 数据库初始化完成！%s\n", ColorGreen, ColorReset)
	fmt.Printf("%s   数据库文件: %s%s\n", ColorCyan, dbPath, ColorReset)
	fmt.Printf("%s   Schema 文件: %s%s\n", ColorCyan, schemaPath, ColorReset)

	return nil
}

// executeSQLStatements 执行SQL语句列表
func executeSQLStatements(db *sql.DB, statements []string) error {
	for i, stmt := range statements {
		stmt = strings.TrimSpace(stmt)

		// 跳过空语句
		if stmt == "" {
			continue
		}

		fmt.Printf("%s正在执行第 %d 条 SQL 语句...%s\n", ColorBlue, i+1, ColorReset)

		// 显示语句预览（前80个字符）
		preview := stmt
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		fmt.Printf("%s语句: %s%s\n", ColorCyan, preview, ColorReset)

		// 执行 SQL 语句
		if _, err := db.Exec(stmt); err != nil {
			// 如果是"already exists"错误，只是警告而不是失败
			if strings.Contains(err.Error(), "already exists") {
				fmt.Printf("%s⚠ 警告: %v%s\n", ColorYellow, err, ColorReset)
				continue
			}
			return fmt.Errorf("执行 SQL 语句失败 (第 %d 条): %v\n语句: %s", i+1, err, stmt)
		}

		fmt.Printf("%s✓ 第 %d 条 SQL 语句执行成功%s\n", ColorGreen, i+1, ColorReset)
	}

	return nil
}
