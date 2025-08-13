package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		dbPath     = flag.String("db", "../data/moox.db", "数据库文件路径")
		sqlDir     = flag.String("sql", "../sql", "SQL文件目录")
		schemaFile = flag.String("schema", "schema.sql", "SQL schema 文件名")
		init       = flag.Bool("init", false, "初始化数据库")
		drop       = flag.Bool("drop", false, "删除数据库文件（危险操作）")
		migrate    = flag.Bool("migrate", false, "执行数据库迁移")
		showHelp   = flag.Bool("help", false, "显示帮助信息")
	)

	flag.Parse()
	if *showHelp {
		showUsage()
		return
	}
	fmt.Printf("%s=== Moox 数据库管理工具 ===%s\n\n", ColorCyan, ColorReset)

	// 删除数据库操作
	if *drop {
		if err := dropDatabase(*dbPath); err != nil {
			fmt.Printf("%s错误: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
		return
	}

	// 初始化数据库操作
	if *init {
		if err := initDatabase(*dbPath, *sqlDir, *schemaFile); err != nil {
			fmt.Printf("%s错误: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
		return
	}

	// 迁移数据库操作
	if *migrate {
		if err := migrateDatabase(*dbPath, *sqlDir, *schemaFile); err != nil {
			fmt.Printf("%s错误: %v%s\n", ColorRed, err, ColorReset)
			os.Exit(1)
		}
		return
	}

	// 如果没有指定操作，显示帮助
	showUsage()
}
