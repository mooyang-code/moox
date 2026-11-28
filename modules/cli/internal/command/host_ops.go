package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/tencenthost"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

func init() {
	hostCmd.AddCommand(
		newHostRebootCommand(loadSetupSnapshot),
		newHostDiagnosticsCommand(loadSetupSnapshot),
	)
}

// newHostRebootCommand 重启无响应的腾讯云主机（自动识别主机所在地域与轻量/CVM 类型）。
func newHostRebootCommand(load func(string) (*setupSnapshot, error)) *cobra.Command {
	var file, hostName string
	var yes bool
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "重启无响应的腾讯云主机（默认只预览，--yes 才重启）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostName)
			if err != nil {
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(host.Provider), "tencent") {
				return fmt.Errorf("reboot_host_provider_unsupported")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.CloudRegion(),
			})
			if err != nil {
				return err
			}
			lighthouse, lighthouseErr := cloudtencent.NewClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.CloudRegion(),
			})
			if lighthouseErr != nil {
				return lighthouseErr
			}
			cloud := tencenthost.TencentCloud{Network: network, Lighthouse: lighthouse}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			instance, err := cloud.LookupHost(ctx, host.Address, cloudtencent.DefaultProbeRegions)
			if err != nil {
				return fmt.Errorf("查询待重启主机失败: %w", err)
			}
			if !yes {
				return writePreview(cmd, map[string]any{
					"action": "reboot_host", "host": host.Name, "instance_id": instance.InstanceID,
					"region": instance.Region, "zone": instance.Zone, "kind": instance.Kind,
				})
			}
			var requestID string
			switch instance.Kind {
			case cloudtencent.KindLighthouse:
				requestID, err = lighthouse.ForRegion(instance.Region).RebootInstance(ctx, instance.InstanceID)
			case cloudtencent.KindCVM:
				cvm, cvmErr := cloudtencent.NewCVMClient(cloudtencent.ClientOptions{
					SecretID:  snapshot.Secrets.TencentCloud.SecretID,
					SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
					Region:    instance.Region,
				})
				if cvmErr != nil {
					return cvmErr
				}
				requestID, err = cvm.RebootInstance(ctx, instance.InstanceID)
			default:
				return fmt.Errorf("reboot_host_kind_unsupported")
			}
			if err != nil {
				return fmt.Errorf("重启主机 %s 失败: %w", host.Name, err)
			}
			return writeSetupJSON(cmd, map[string]any{
				"status": "reboot_requested", "host": host.Name,
				"instance_id": instance.InstanceID, "region": instance.Region,
				"zone": instance.Zone, "request_id": requestID,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostName, "host", "", "目标主机名称")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认重启；不加只预览目标实例")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newHostDiagnosticsCommand(load func(string) (*setupSnapshot, error)) *cobra.Command {
	var file, hostName string
	cmd := &cobra.Command{
		Use:   "diagnostics",
		Short: "检查远端主机磁盘和服务进程",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostName)
			if err != nil {
				return err
			}
			transport, err := dialSetupHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			defer transport.Close()
			root := snapshot.Manifest.Paths.DeployRoot
			result, err := transport.Run(cmd.Context(), []string{"sh", "-lc", `set -eu
root="$1"
printf 'df:\n'
df -h "$root" /
printf 'processes:\n'
ps -eo pid,stat,cmd | grep -E 'moox-' | grep -v grep || true
printf 'sizes:\n'
du -xh --max-depth=2 "$root/data" "$root/logs" 2>/dev/null | sort -h | tail -40 || true
printf 'services:\n'
systemctl --user list-units 'moox-*' --no-pager --no-legend 2>/dev/null || true
`, "moox-host-diagnostics", root}, nil)
			if err != nil {
				return fmt.Errorf("host_diagnostics_failed: %v", err)
			}
			return writeSetupJSON(cmd, map[string]any{"host": host.Name, "root": root, "diagnostics": result.Stdout})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostName, "host", "control", "主机名称")
	return cmd
}
