package command

import (
	"fmt"
	"strings"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	"github.com/spf13/cobra"
)

// newSetupSyncDeploymentsCommand 把 moox.toml 里某台主机的完整组件部署同步给 Admin。
// 部署流程已经会在安装时同步；服务目录新增了可选组件（例如 archive-mgr）而主机并没有重新部署时，用它单独同步。
func newSetupSyncDeploymentsCommand(deps setupDeps) *cobra.Command {
	var file, host string
	var yes bool
	cmd := &cobra.Command{Use: "sync-deployments", Short: "把 moox.toml 中指定主机的组件部署同步给 Admin（生成网关路由；默认只预览，--yes 才执行）", Args: cobra.NoArgs,
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
			if !yes {
				return writePreview(cmd, map[string]any{"action": "sync_deployments", "host": host, "components": snapshot.Manifest.Deployments[host]})
			}
			gateway, err := openCommandGateway(cmd.Context(), file, snapshot)
			if err != nil {
				return err
			}
			defer gateway.Close()
			if err := setupclient.New(gateway).SyncHostDeployments(cmd.Context(), snapshot, host); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ok", "host": host, "components": snapshot.Manifest.Deployments[host]})
		}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID（moox.toml 里的主机名称）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认同步；不加只预览")
	return cmd
}
