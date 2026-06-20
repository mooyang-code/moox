package cmd

import "github.com/spf13/cobra"

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"存储"},
	Short:   "存储数据导入与读写工具",
	Long:    "存储数据导入与读写工具。历史数据导入请使用 moox-cli storage import --format csv。",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(storageCmd)
}
