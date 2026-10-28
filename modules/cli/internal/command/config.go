package command

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(newConfigCommand())
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "管理本机的 moox.toml 与告警渠道",
		Long: `config init 在本机打开一次性表单，填写部署信息并生成加密保存的 moox.toml；
config edit 在本机打开配置页，查看并修改 moox.toml（保存后重新加密）；
config encrypt 把明文保存的 moox.toml 加密保存；
config migrate 把 moox.toml 改写成当前格式（默认只预览，加 --yes 写回）；
config backup 把配置用管理员密码加密备份到管理后台，config restore 换电脑时从管理台恢复；
config notification 对比 moox.toml 与 Monitor 的告警渠道（唯一来源），确认后写入 Monitor，并显示各主机策略缓存的版本。`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newConfigInitCommand(), newConfigEditCommand(), newConfigEncryptCommand(), newConfigMigrateCommand(), newConfigGetCommand(), newConfigSetCommand(), newConfigBackupCommand(), newConfigRestoreCommand(), newConfigNotificationCommand(defaultNotificationDeps()))
	return cmd
}
