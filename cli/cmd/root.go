/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"

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
	Use:   "moox",
	Short: "moox 是一站式量化管理平台的命令行工具",
	Long:  "moox 是一站式量化管理平台的命令行工具，提供数据管理、策略回测、模型训练等量化投资相关功能。",
	Run: func(cmd *cobra.Command, args []string) {
		// 如果设置了version标志，显示版本信息并退出
		if versionFlag {
			showVersionInfo()
			return
		}

		// 显示LOGO
		showLogo()

		// 加载配置
		var err error
		AppConfig, err = config.LoadConfig()
		if err != nil {
			fmt.Printf("加载配置失败: %v\n", err)
			os.Exit(1)
		}

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
	if BuildTime == "" {
		BuildTime = "unknown"
	}
	if GitCommit == "" {
		GitCommit = "unknown"
	}

	return fmt.Sprintf(`moox CLI %s
构建时间: %s
Git提交: %s`,
		Version,
		BuildTime,
		GitCommit)
}

// showVersionInfo 显示版本信息
func showVersionInfo() {
	fmt.Println(GetFullVersionInfo())
}

// showLogo 显示MOOX的ASCII艺术LOGO
func showLogo() {
	logo := `
   ███╗   ███╗  ██████╗   ██████╗  ██╗  ██╗
   ████╗ ████║ ██╔═══██╗ ██╔═══██╗ ╚██╗██╔╝
   ██╔████╔██║ ██║   ██║ ██║   ██║  ╚███╔╝ 
   ██║╚██╔╝██║ ██║   ██║ ██║   ██║  ██╔██╗ 
   ██║ ╚═╝ ██║ ╚██████╔╝ ╚██████╔╝ ██╔╝ ██╗
   ╚═╝     ╚═╝  ╚═════╝   ╚═════╝  ╚═╝  ╚═╝
                                            
   🚀 一站式量化管理平台 - 让投资决策更智能 📊
   
`
	fmt.Print(logo)
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	// 先执行命令解析，这样可以检查是否使用了--version标志
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	// 添加版本标志
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "显示版本信息")
}
