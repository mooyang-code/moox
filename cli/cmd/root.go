/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/mooyang-code/moox/cli/internal/config"

	"github.com/spf13/cobra"
)

// 版本信息变量的引用
var (
	Version   string
	BuildTime string
	GitCommit string
)

// 版本标志
var versionFlag bool

// 全局配置变量
var AppConfig *config.Config

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "moox-cli",
	Short: "moox-cli 是一站式量化管理平台的命令行工具",
	Long:  "moox-cli 是一站式量化管理平台的命令行工具，提供系统初始、数据管理、用户注册等功能。",
	Run: func(cmd *cobra.Command, args []string) {
		// 如果设置了version标志，显示版本信息并退出
		if versionFlag {
			showVersionInfo()
			return
		}

		// 显示LOGO
		showLogo()

		// 否则显示帮助信息
		cmd.Help()
	},
}

// GetVersionInfo 返回格式化的版本信息
func GetVersionInfo() string {
	if Version == "" {
		Version = "dev"
	}
	return fmt.Sprintf("moox CLI %s", Version)
}

// GetFullVersionInfo 返回完整的版本信息
func GetFullVersionInfo() string {
	if Version == "" {
		Version = "dev"
	}

	return fmt.Sprintf("moox CLI %s", Version)
}

// showVersionInfo 显示版本信息
func showVersionInfo() {
	fmt.Println(GetFullVersionInfo())
}

// padLine 确保行内容达到指定宽度
func padLine(content string, width int) string {
	displayWidth := calculateDisplayWidth(content)
	padding := width - displayWidth
	if padding > 0 {
		return content + fmt.Sprintf("%*s", padding, "")
	}
	return content
}

// calculateDisplayWidth 计算字符串的显示宽度
func calculateDisplayWidth(content string) int {
	displayWidth := 0
	inAnsiSequence := false

	runes := []rune(content)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// 检测ANSI转义序列开始
		if r == '\033' && i+1 < len(runes) && runes[i+1] == '[' {
			inAnsiSequence = true
			continue
		}

		// 跳过ANSI序列中的字符
		if inAnsiSequence {
			if r == 'm' || r == 'K' || r == 'J' || r == 'H' {
				inAnsiSequence = false
			}
			continue
		}

		// 计算实际显示宽度
		if r <= 127 {
			displayWidth++ // ASCII字符
		} else if (r >= 0x4E00 && r <= 0x9FFF) || // CJK统一汉字
			(r >= 0x3400 && r <= 0x4DBF) || // CJK扩展A
			(r >= 0x20000 && r <= 0x2A6DF) || // CJK扩展B
			(r >= 0x2A700 && r <= 0x2B73F) || // CJK扩展C
			(r >= 0x2B740 && r <= 0x2B81F) || // CJK扩展D
			(r >= 0x2B820 && r <= 0x2CEAF) || // CJK扩展E
			(r >= 0x2CEB0 && r <= 0x2EBEF) || // CJK扩展F
			(r >= 0x1F600 && r <= 0x1F64F) || // 表情符号
			(r >= 0x1F300 && r <= 0x1F5FF) || // 各种符号
			(r >= 0x1F680 && r <= 0x1F6FF) || // 交通符号
			(r >= 0x1F1E6 && r <= 0x1F1FF) { // 区域指示符号
			displayWidth += 2 // 宽字符（中文、emoji等）
		} else {
			displayWidth++ // 其他Unicode字符（包括框线字符等）
		}
	}

	return displayWidth
}

// showLogo 显示MOOX的ASCII艺术LOGO
func showLogo() {
	// 定义标题行
	titleLine := "╭─ Moox CLI ─────────────────────────────────────────────────────────╮"

	// 动态计算内容区域宽度（标题行宽度减去左右边框字符）
	width := calculateDisplayWidth(titleLine) - 2

	fmt.Println()
	fmt.Println(titleLine)
	fmt.Printf("│%s│\n", padLine("", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ███╗   ███╗  ██████╗   ██████╗  ██╗  ██╗     ██████╗██╗     ██╗\033[0m", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ████╗ ████║ ██╔═══██╗ ██╔═══██╗ ╚██╗██╔╝    ██╔════╝██║     ██║\033[0m", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ██╔████╔██║ ██║   ██║ ██║   ██║  ╚███╔╝     ██║     ██║     ██║\033[0m", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ██║╚██╔╝██║ ██║   ██║ ██║   ██║  ██╔██╗     ██║     ██║     ██║\033[0m", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ██║ ╚═╝ ██║ ╚██████╔╝ ╚██████╔╝ ██╔╝ ██╗    ╚██████╗███████╗██║\033[0m", width))
	fmt.Printf("│%s│\n", padLine(" \033[92m ╚═╝     ╚═╝  ╚═════╝   ╚═════╝  ╚═╝  ╚═╝     ╚═════╝╚══════╝╚═╝\033[0m", width))
	fmt.Printf("│%s│\n", padLine("", width))
	fmt.Printf("│%s│\n", padLine("        🚀 一站式量化管理平台 - 让投资决策更智能 📊", width))

	// 动态生成底部边框，与标题行宽度匹配
	bottomLine := "╰" + strings.Repeat("─", width) + "╯"
	fmt.Println(bottomLine)

	// 以下内容不再使用边框格式，直接输出
	fmt.Println()
	fmt.Println("🛠️  可用命令:")
	fmt.Println("    🔐 auth (认证)         用户注册、登录、密码管理")
	fmt.Println("    🗄️  db (数据库)         数据库初始化、删除、元数据表管理、数据查询操作")
	fmt.Println("    📦 storage (存储)      高性能数据读写服务")
	fmt.Println("    📨 msg (消息队列)       实时消息处理和队列管理")
	fmt.Println()
	fmt.Println("📖 使用帮助:")
	fmt.Println("    📚 查看命令帮助       ./moox-cli --help")
	fmt.Println("    🔐 用户注册          ./moox-cli auth register")
	fmt.Println("    🗄️ 初始化数据库      ./moox-cli db --init")
	fmt.Println("    🗄️ 数据库操作        ./moox-cli db --help")
	fmt.Println("    🌐 配置示例          config/cli-example.yaml")
	fmt.Println()
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	// 自定义help命令，改为中文描述
	helpCmd := &cobra.Command{
		Use:   "help [command]",
		Short: "查看命令帮助信息",
		Long:  "查看任意命令的详细帮助信息",
		Run: func(c *cobra.Command, args []string) {
			cmd, _, e := c.Root().Find(args)
			if cmd == nil || e != nil {
				c.Root().Usage()
			} else {
				cmd.Help()
			}
		},
	}
	rootCmd.SetHelpCommand(helpCmd)

	// 先执行命令解析，这样可以检查是否使用了--version标志
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	// 添加版本标志
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "显示版本信息")

	// 禁用默认的completion命令
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// 在初始化阶段加载配置，确保所有子命令都能使用
	loadGlobalConfig()
}

// loadGlobalConfig 加载全局配置
func loadGlobalConfig() {
	var err error
	AppConfig, err = config.LoadConfig()
	if err != nil {
		fmt.Printf("%v\n", err)
		fmt.Println("\033[93m💡 将使用默认配置，某些功能可能无法正常工作\033[0m")
		// 创建默认配置，避免panic
		AppConfig = &config.Config{}
		if AppConfig.Moox == nil {
			AppConfig.Moox = &config.MooxConfig{}
		}
	}
}
