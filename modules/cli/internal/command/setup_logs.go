package command

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const maxSetupLogLines = 2000

func newSetupLogsCommand(deps setupDeps) *cobra.Command {
	var file, host, component string
	var lines int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "只读查看组件在主机上的日志，包括失败回滚的发布目录，密钥字段已遮掉",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if lines < 1 || lines > maxSetupLogLines {
				return fmt.Errorf("--lines 要在 1 到 %d 之间", maxSetupLogLines)
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			target := snapshot.Manifest.HostByID(host)
			if target.Name == "" {
				return fmt.Errorf("moox.toml 里没有主机 %q", host)
			}
			catalog, err := servicecatalog.LoadEmbedded()
			if err != nil {
				return err
			}
			entry, found := catalog.Component(component)
			if !found {
				return fmt.Errorf("服务目录里没有组件 %q", component)
			}
			if entry.Scope != "host" && !slices.Contains(snapshot.Manifest.Deployments[host], component) {
				return fmt.Errorf("组件 %s 没有部署在主机 %s 上", component, host)
			}
			if deps.readLogs == nil {
				return errors.New("setup logs dependencies are unavailable")
			}
			sections, err := deps.readLogs(cmd.Context(), target, snapshot.Manifest.Paths.Resolved().DeployRoot, component, entry.Doctor.Unit, lines)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"host": host, "component": component, "logs": sections})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID")
	cmd.Flags().StringVar(&component, "component", "", "组件 ID，比如 monitor、access")
	cmd.Flags().IntVar(&lines, "lines", 100, "每份日志看最后多少行")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("component")
	return cmd
}

func defaultSetupReadLogs(ctx context.Context, host setupconfig.Host, root, component, unit string, lines int) ([]deployscan.LogSection, error) {
	return deployscan.ReadLogs(ctx, dialSetupHost, host, root, component, unit, lines)
}
