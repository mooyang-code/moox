package command

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
)

func newConfigMigrateCommand() *cobra.Command {
	var file, renameHost, importSCF, tlsMode string
	var yes bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "把 moox.toml 改写成当前格式（默认只预览）",
		Long: `在本机解密 moox.toml 并改写成当前格式：
- [[factors.definitions]] 只保留 factor_id，file 指向因子库（factors.source_dir）里包含该因子的 YAML 文件，删除其余旧字段；
- 给 control、storage、overseas、factor 主机补上中文名 label（已有的不动）；
- 把 [deployments] 里已改名的旧组件身份改成当前名字（cloudnode → cloudfunction）；
- 加 --rename-host 旧ID=新ID 时把主机 ID 改名（主机表、[deployments]、access_host 与 host 引用）；
- 加 --import-scf <JSON> 时整体替换 [scf_plan]（云账号、地域网络与入口主机、空间与市场），并给入口主机补上 region 与 private_address。
改写后先按当前格式校验。默认只预览改动；加 --yes 才写回：原文件备份为 moox.toml.bak-<时间>，新内容重新加密保存。
输出只有键名、因子 ID、文件名、主机 ID 与中文名，不输出任何配置值。
注意：改主机 ID 只改本机配置；生产主机要按 docs/部署与运维.md「主机命名」重新登记。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := configinit.MigrateOptions{Write: yes, SCFImport: importSCF}
			if tlsMode != "" {
				host, mode, ok := strings.Cut(tlsMode, "=")
				if !ok {
					return errors.New("--set-tls-mode 需要写成 主机ID=auto|public|internal")
				}
				opts.TLSModeHost, opts.TLSMode = strings.TrimSpace(host), strings.TrimSpace(mode)
			}
			if renameHost != "" {
				from, to, ok := strings.Cut(renameHost, "=")
				if !ok {
					return errors.New("--rename-host 需要写成 旧ID=新ID")
				}
				opts.RenameFrom, opts.RenameTo = strings.TrimSpace(from), strings.TrimSpace(to)
			}
			absolute, err := filepath.Abs(file)
			if err != nil {
				return err
			}
			opts.RepoRoot = filepath.Dir(absolute)
			report, err := configinit.MigrateFile(file, opts)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, report)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径（因子库目录相对它所在的仓库根目录解析）")
	cmd.Flags().StringVar(&renameHost, "rename-host", "", "把主机 ID 改名，写成 旧ID=新ID，例如 legacy-1=overseas")
	cmd.Flags().StringVar(&importSCF, "import-scf", "", "用这份 JSON 整体替换 [scf_plan]，并给入口主机补上 region 与 private_address")
	cmd.Flags().StringVar(&tlsMode, "set-tls-mode", "", "设置主机的控制台证书模式，写成 主机ID=auto|public|internal")
	cmd.Flags().BoolVar(&yes, "yes", false, "校验通过后写回（备份原文件并重新加密）")
	return cmd
}
