package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"存储"},
	Short:   "旧存储命令已迁移",
	Long:    "旧 storage 命令已迁移到 data 命令。请使用 moox-cli data csv import 或 moox-cli data rows export。",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("旧 storage 命令已迁移，请使用: moox-cli data --help")
	},
}

func init() {
	rootCmd.AddCommand(storageCmd)
}
