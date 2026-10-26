package command

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
)

func newConfigMigrateCommand() *cobra.Command {
	var file, renameHost, importSCF, tlsMode, addDeployment, reachability, factorsEnabled, retention string
	var yes bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "把 moox.toml 改写成当前格式（默认只预览）",
		Long: `在本机解密 moox.toml 并改写成当前格式：
- [[factors.definitions]] 只保留 factor_id，file 指向因子库（factors.source_dir）里包含该因子的 YAML 文件，删除其余旧字段；
- 给 control、storage、overseas、factor 主机补上中文名 label（已有的不动）；
- 把 [deployments] 里已改名的旧组件身份改成当前名字（cloudnode → cloudfunction）；
- 加 --rename-host 旧ID=新ID 时把主机 ID 改名（主机表、[deployments]、access_host 与 host 引用）；
- 加 --set-factors-enabled true|false 时设置 [factors] enabled；
- 加 --set-retention defaults.<频率>=<时长> 或 <空间>.<频率>=<时长> 时设置该频率的保留期（生效要重装 Storage 单元）；
- 加 --set-reachability 主机ID=direct|outbound 时设置主机可达性；
- 加 --add-deployment 主机ID=组件ID[,组件ID] 时把组件并入该主机的 [deployments]（已有的不重复）；
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
			opts.FactorsEnabled = strings.TrimSpace(factorsEnabled)
			if retention != "" {
				target, value, ok := strings.Cut(retention, "=")
				if !ok {
					return errors.New("--set-retention 需要写成 defaults.<频率>=<时长>，例如 defaults.1m=35d")
				}
				opts.RetentionTarget, opts.RetentionValue = strings.TrimSpace(target), strings.TrimSpace(value)
			}
			if reachability != "" {
				host, mode, ok := strings.Cut(reachability, "=")
				if !ok {
					return errors.New("--set-reachability 需要写成 主机ID=direct|outbound")
				}
				opts.ReachabilityHost, opts.Reachability = strings.TrimSpace(host), strings.TrimSpace(mode)
			}
			if addDeployment != "" {
				host, list, ok := strings.Cut(addDeployment, "=")
				if !ok {
					return errors.New("--add-deployment 需要写成 主机ID=组件ID[,组件ID]")
				}
				opts.AddDeploymentHost, opts.AddDeploymentComponents = strings.TrimSpace(host), strings.Split(list, ",")
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
	cmd.Flags().StringVar(&factorsEnabled, "set-factors-enabled", "", "设置 [factors] enabled，取 true 或 false（true 时 setup init / setup factors 才会导入因子）")
	cmd.Flags().StringVar(&retention, "set-retention", "", "设置某个频率的保留期，写成 defaults.<频率>=<时长> 或 <空间>.<频率>=<时长>，例如 defaults.1m=35d")
	cmd.Flags().StringVar(&reachability, "set-reachability", "", "设置主机的可达性，写成 主机ID=direct|outbound（只能出站的内网机器用 outbound）")
	cmd.Flags().StringVar(&addDeployment, "add-deployment", "", "把组件加进主机的 [deployments]，写成 主机ID=组件ID[,组件ID]，例如 factor=factor-engine,archive-worker")
	cmd.Flags().StringVar(&tlsMode, "set-tls-mode", "", "设置主机的控制台证书模式，写成 主机ID=auto|public|internal")
	cmd.Flags().BoolVar(&yes, "yes", false, "校验通过后写回（备份原文件并重新加密）")
	return cmd
}
