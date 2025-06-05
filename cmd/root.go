/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"

	"moox/config"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "moox",
	Short: "moox 是一个用于操作数据库的命令行工具",
	Long:  "moox 是一个支持多种数据库操作的命令行工具，包括创建表、删除表等功能。",
}

// 全局配置变量
var AppConfig *config.Config

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
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
