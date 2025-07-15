package main

import "fmt"

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

// showUsage 显示使用帮助信息
func showUsage() {
	fmt.Printf("%sMooX 命令行工具%s\n\n", ColorGreen, ColorReset)
	fmt.Println("用法:")
	fmt.Printf("  %s./moox-cli -init%s       初始化数据库（创建表和索引）\n", ColorYellow, ColorReset)
	fmt.Printf("  %s./moox-cli -migrate%s    执行数据库迁移\n", ColorYellow, ColorReset)
	fmt.Printf("  %s./moox-cli -drop%s       删除数据库文件（危险操作）\n", ColorYellow, ColorReset)
	fmt.Printf("  %s./moox-cli -help%s       显示此帮助信息\n", ColorYellow, ColorReset)
	fmt.Println("\n参数:")
	fmt.Printf("  %s-db%s       数据库文件路径 (默认: ./data/auth.db)\n", ColorBlue, ColorReset)
	fmt.Printf("  %s-sql%s      SQL文件目录 (默认: ./sql)\n", ColorBlue, ColorReset)
	fmt.Printf("  %s-schema%s   SQL schema 文件名 (默认: schema.sql)\n", ColorBlue, ColorReset)
	fmt.Println("\n示例:")
	fmt.Printf("  %s./moox-cli -init%s                     # 使用默认路径初始化\n", ColorCyan, ColorReset)
	fmt.Printf("  %s./moox-cli -init -db=./data/auth.db%s  # 指定数据库路径\n", ColorCyan, ColorReset)
	fmt.Printf("  %s./moox-cli -init -schema=custom.sql%s  # 指定 schema 文件名\n", ColorCyan, ColorReset)
	fmt.Printf("  %s./moox-cli -migrate -sql=./sql%s       # 执行迁移\n", ColorCyan, ColorReset)
}
