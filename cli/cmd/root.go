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

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "moox",
	Short: "moox 是一站式量化管理平台的命令行工具",
	Long:  "moox 是一站式量化管理平台的命令行工具，提供数据管理、策略回测、模型训练等量化投资相关功能。",
}

// 全局配置变量
var AppConfig *config.Config

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
	// 显示LOGO
	showLogo()

	// 加载配置
	var err error
	AppConfig, err = config.LoadConfig()
	if err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
