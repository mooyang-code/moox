package command

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(newConfigCommand())
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "管理本机的 moox.toml 与 moox.secrets.toml",
		Long: `部署配置拆成两份文件，放在同一目录：明文的 moox.toml（拓扑与部署参数，直接编辑即可）和加密的 moox.secrets.toml（秘密的初始值）。
config init 在本机打开一次性表单，填写部署信息，生成这两份文件；
config edit 在本机打开配置页，查看并修改两份文件（秘密文件保存后重新加密）；
config get / config set 读取、修改其中一份里的单个值（--file 指定哪一份，秘密默认打码）；
config encrypt 把明文保存的 moox.secrets.toml 加密保存。
换电脑时把两份文件一起拷过去，再带上解密秘密文件的密钥（系统钥匙串里的，或 ~/.config/moox/config.key）。`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newConfigInitCommand(), newConfigEditCommand(), newConfigEncryptCommand(), newConfigGetCommand(), newConfigSetCommand())
	return cmd
}
