package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
func initDatabase(dbPath, sqlDir, schemaFile string) error {
	fmt.Printf("%s正在初始化数据库: %s%s\n", ColorBlue, dbPath, ColorReset)

	// 确保数据目录存在
	dataDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %v", err)
	}
	fmt.Printf("%s✓ 数据目录已准备: %s%s\n", ColorGreen, dataDir, ColorReset)

	// 构建 schema 文件路径
	var schemaPath string
	if filepath.IsAbs(schemaFile) {
		// 如果是绝对路径，直接使用
		schemaPath = schemaFile
	} else {
		// 如果是相对路径，与 sqlDir 组合
		schemaPath = filepath.Join(sqlDir, schemaFile)
	}

	// 读取 schema 文件
	statements, err := readSQLFromFile(schemaPath)
	if err != nil {
		return fmt.Errorf("读取 schema 文件失败: %v", err)
	}
	fmt.Printf("%s✓ 已读取 SQL schema 文件: %s (共 %d 条语句)%s\n", ColorGreen, schemaPath, len(statements), ColorReset)

	// 连接数据库
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("连接数据库失败: %v", err)
	}
	fmt.Printf("%s✓ 数据库连接成功%s\n", ColorGreen, ColorReset)

	// 获取底层 SQL DB 连接
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取 SQL DB 连接失败: %v", err)
	}
	defer sqlDB.Close()

	// 执行 SQL 语句
	if err := executeSQLStatements(sqlDB, statements); err != nil {
		return fmt.Errorf("执行 SQL 语句失败: %v", err)
	}

	fmt.Printf("%s🎉 数据库初始化完成！%s\n", ColorGreen, ColorReset)
	fmt.Printf("%s   数据库文件: %s%s\n", ColorCyan, dbPath, ColorReset)
	fmt.Printf("%s   Schema 文件: %s%s\n", ColorCyan, schemaPath, ColorReset)

	return nil
}

// migrateDatabase 迁移数据库
func migrateDatabase(dbPath, sqlDir, schemaFile string) error {
	fmt.Printf("%s正在执行数据库迁移...%s\n", ColorBlue, ColorReset)

	// 检查数据库是否存在
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Printf("%s数据库不存在，将先执行初始化%s\n", ColorYellow, ColorReset)
		return initDatabase(dbPath, sqlDir, schemaFile)
	}

	// 这里可以添加更复杂的迁移逻辑
	// 目前简单重新应用 schema
	return initDatabase(dbPath, sqlDir, schemaFile)
}
