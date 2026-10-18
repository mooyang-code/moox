package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
)

// newSetupRemoveHostCommand 从 Admin 拓扑删除一台已不在 moox.toml 里的主机（例如改名前的旧 ID）。
// 只改 Admin 的登记：业务组件放置与网关路由撤销、主机删除、控制台 SSH 记录删除；不碰目标机上的进程与文件。
func newSetupRemoveHostCommand(deps setupDeps) *cobra.Command {
	var file, host string
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove-host",
		Short: "从 Admin 删除一台已不在 moox.toml 里的主机（默认只预览）",
		Long: `用于主机改名或下线：先在 moox.toml 里去掉（或改名）这台主机，再运行本命令删除 Admin 里的旧登记。
默认只预览这台主机在 Admin 里的地址、组件放置与控制台 SSH 记录；加 --yes 才执行：清空业务组件放置（网关路由随之撤销）、
删除主机（它的网关签名身份随即失效）、删除控制台 SSH 终端里同名的记录。
不会停止或删除目标机上的进程与文件，执行前要先在目标机上停掉这台主机的全部组件。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			host = strings.TrimSpace(host)
			if host == "" {
				return fmt.Errorf("--host 必填")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if host == snapshot.Manifest.ControlHost().Name {
				return fmt.Errorf("不能删除控制机 %s", host)
			}
			if _, configured := snapshot.Manifest.HostCatalog[host]; configured {
				return fmt.Errorf("主机 %s 还在 moox.toml 里；先把它从 moox.toml 去掉或改名，再删除 Admin 里的登记", host)
			}
			gateway, err := openCommandGateway(cmd.Context(), file, snapshot)
			if err != nil {
				return err
			}
			defer gateway.Close()
			client := setupclient.New(gateway)
			registered, found, err := client.RegisteredHostByID(cmd.Context(), host)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("Admin 里没有主机 %s", host)
			}
			if !yes {
				return writeSetupJSON(cmd, map[string]any{"status": "preview", "host": registered, "next_action": "先停掉目标机上这台主机的全部组件，确认后加 --yes 执行"})
			}
			if err := client.RemoveRegisteredHost(cmd.Context(), registered); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "removed", "host": registered})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "要删除的主机 ID（已不在 moox.toml 里）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认执行删除")
	return cmd
}
