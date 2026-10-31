package command

import (
	"errors"
	"fmt"
	"os"

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

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "moox-cli",
	Short: "moox-cli 是 MooX 量化系统的命令行工具",
	Long: `moox-cli 是 MooX 量化系统的命令行工具：管理台上的操作和各模块的主要操作都有对应的命令，Agent 通过 moox skill 使用它们。

所有命令默认读取当前目录的 moox.toml（--file 指定），经里面登记的 SSH 配置连接各台主机，不需要手工建隧道或设置环境变量。
写操作里的破坏性命令（删除、重启、改防火墙、切换线上版本等）默认只预览，确认后加 --yes 才执行。
没有专用命令的接口用 gateway call；gateway methods 列出可调用的方法。`,
	Run: func(cmd *cobra.Command, args []string) {
		// 如果设置了version标志，显示版本信息并退出
		if versionFlag {
			showVersionInfo()
			return
		}
		cmd.Help()
	},
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
	mountRPCCommands(rootCmd)
	err := rootCmd.Execute()
	closeModuleHealth()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) {
			os.Exit(coded.ExitCode())
		}
		os.Exit(1)
	}
}

func init() {
	// 添加版本标志
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "显示版本信息")

	// 禁用默认的completion命令
	rootCmd.CompletionOptions.DisableDefaultCmd = true

}
