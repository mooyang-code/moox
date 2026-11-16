package command

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
)

func newConfigGetCommand() *cobra.Command {
	var file string
	var reveal bool
	cmd := &cobra.Command{
		Use:   "get [路径]",
		Short: "读取 moox.toml 或 moox.secrets.toml 里的配置（--file 指定哪一份，密钥类默认打码）",
		Long: `按路径输出 moox.toml（明文）或 moox.secrets.toml（在本机解密）里对应的值（JSON），--file 指定读哪一份，默认 ./moox.toml。
路径用点号分隔，例如 hosts.storage、storage_retention.defaults.5m、deployments.control.0（数组用序号）；不写路径输出整份。
password、secret、token、private_key 等密钥类的值显示为 ***，加 --reveal 才输出明文：只在确实需要时用，不要把明文贴进对话、日志或文档。`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			value, err := configinit.GetConfig(file, target, reveal, nil)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, value)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 或 moox.secrets.toml 的路径")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "输出密钥类的明文")
	return cmd
}

func newConfigSetCommand() *cobra.Command {
	var file string
	var yes bool
	cmd := &cobra.Command{
		Use:   "set 表名.键=值",
		Short: "修改 moox.toml 或 moox.secrets.toml 里一个已有的单值配置（默认只预览）",
		Long: `只改指定的一个已有单值键，注释和其余内容原样保留，按这份文件的格式校验后才写回；--file 指定改哪一份，默认 ./moox.toml。
写法：表名.键=值，例如 hosts.storage.label=存储机、storage_retention.defaults.5m=240d，秘密文件里的 admin.username=ops；根部的键直接写键名。
值按 true/false、整数、小数识别，其余按字符串。数组、数组表（[[...]]）、不存在的表或键不能用它改：moox.toml 直接编辑，秘密文件用 config edit（网页表单）。
默认只预览；加 --yes 才写回：原文件备份为 <文件名>.bak-<时间>，moox.toml 写回明文，秘密文件重新加密。输出里不含任何配置值。`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, value, ok := strings.Cut(args[0], "=")
			if !ok {
				return errors.New("需要写成 表名.键=值，例如 hosts.storage.label=存储机")
			}
			report, err := configinit.SetConfig(file, strings.TrimSpace(target), value, yes, nil, nil)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, report)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 或 moox.secrets.toml 的路径")
	cmd.Flags().BoolVar(&yes, "yes", false, "校验通过后写回（备份原文件；秘密文件重新加密）")
	return cmd
}
