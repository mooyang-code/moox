package command

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
	"github.com/spf13/cobra"
)

// 配置页、加密、备份与恢复的入口，测试里替换。
var (
	runConfigEditor   = configinit.RunEditor
	encryptConfigFile = func(path string) (bool, error) { return configinit.EncryptFile(path, nil) }
	backupConfigFile  = func(ctx context.Context, path string) (configinit.BackupResult, error) {
		return configinit.BackupFile(ctx, path, nil)
	}
	runConfigRestore = configinit.RunRestore
)

func newConfigEditCommand() *cobra.Command {
	var file string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "在本机打开配置页，查看并修改 moox.toml",
		Long: `在本机起一个只监听 127.0.0.1 的一次性页面，并用系统浏览器打开。
页面展示当前配置（密码和密钥默认隐藏，点「显示」才出现），可以直接修改配置内容；
保存前完整校验，校验通过后重新加密、原子替换文件。用户点「完成」或超时后命令结束。

页面上的内容只在本机处理，不会经过 Agent：命令只输出是否保存过、文件是否加密。
如果在 Agent 里运行，请让用户在弹出的浏览器页面里操作，Agent 不要打开或读取这个页面。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runConfigEditor(cmd.Context(), configinit.EditorOptions{File: file, Timeout: timeout, Notice: cmd.ErrOrStderr()})
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "closed", "path": result.Path, "saved": result.Saved, "encrypted": result.Encrypted})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	cmd.Flags().DurationVar(&timeout, "timeout", configinit.DefaultTimeout, "页面有效期，超时自动关闭")
	return cmd
}

func newConfigEncryptCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "encrypt",
		Short: "把明文保存的 moox.toml 加密保存",
		Long: `用本机密钥加密 moox.toml（内容不变）：密钥存放在系统钥匙串（macOS 钥匙串、Windows 凭据管理器、
Linux Secret Service），没有钥匙串的机器存放在 ~/.config/moox/config.key（0600）。
已经加密的文件只核对能否解开，不做改动。命令不输出任何配置内容。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			already, err := encryptConfigFile(file)
			if err != nil {
				return err
			}
			status := "encrypted"
			if already {
				status = "already_encrypted"
			}
			return writeSetupJSON(cmd, map[string]any{"status": status, "path": file})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	return cmd
}

func newConfigBackupCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "把 moox.toml 加密备份到自己的管理后台",
		Long: `用 moox.toml 里的管理员账号登录管理台（管理台服务器的 9527 端口），用管理员密码加密整份配置后上传。
服务端只保存密文，没有管理员密码解不开；换电脑时用 moox-cli config restore 恢复。
初始化部署完成后运行一次；之后在配置页保存时会自动同步。命令不输出任何配置内容。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := backupConfigFile(cmd.Context(), file)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "backed_up", "console": result.Console, "updated_at": result.UpdatedAt.UTC().Format(time.RFC3339)})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	return cmd
}

func newConfigRestoreCommand() *cobra.Command {
	var output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "换电脑时从管理台恢复 moox.toml",
		Long: `在本机起一个只监听 127.0.0.1 的一次性页面：用户填管理台地址和管理员账号，CLI 登录后取回配置备份，
在本机用管理员密码解开，再用这台电脑的密钥加密写成 moox.toml（权限 0600，不覆盖已有文件）。
管理台证书不被这台电脑信任时，页面会请用户核对证书指纹。命令只输出写到了哪里。
如果在 Agent 里运行，请让用户在弹出的浏览器页面里填写，Agent 不要打开或读取这个页面。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runConfigRestore(cmd.Context(), configinit.RestoreOptions{Output: output, Timeout: timeout, Notice: cmd.ErrOrStderr()})
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "restored", "path": result.Path, "console": result.Console})
		},
	}
	cmd.Flags().StringVar(&output, "output", defaultSetupFile, "恢复出的 moox.toml 路径；已存在时拒绝覆盖")
	cmd.Flags().DurationVar(&timeout, "timeout", configinit.DefaultTimeout, "页面有效期，超时自动关闭")
	return cmd
}
