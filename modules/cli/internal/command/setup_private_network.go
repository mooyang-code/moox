package command

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

func newSetupPrivateNetworkCommand(deps setupDeps) *cobra.Command {
	return newPrivateNetworkCommand("private-network", deps)
}

func newPrivateNetworkCommand(use string, deps setupDeps) *cobra.Command {
	var file, probeRegions string
	var dryRun, skipProbe bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "发现主机的网络拓扑并探测公网端口（只读）",
		Long: `读取 moox.toml 中 provider=tencent 的主机，发现它们所在的地域、VPC、子网和私网地址，并从每台主机探测
其他主机的公网端口。只读：不创建云联网，不修改主机或 SCF。SCF 采集函数的路由见 moox-cli setup scf-network-plan。

SSH 和控制台入口继续使用公网 IP。不会调用 ModifyInstancesVpcAttribute。

示例：
  moox-cli setup private-network --file ./moox.toml --dry-run
  moox-cli setup private-network --file ./moox.toml --skip-probe`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			opts := privatenet.Options{
				HomeRegion: snapshot.Manifest.TencentCloud.Region,
				DryRun:     dryRun,
				SkipProbe:  skipProbe,
			}
			if strings.TrimSpace(probeRegions) != "" {
				opts.ProbeRegions = splitCSV(probeRegions)
			}
			result, err := deps.ensurePrivateNetwork(cmd.Context(), snapshot, opts, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&probeRegions, "probe-regions", "", "额外探测地域，逗号分隔；默认含广州/香港/上海/北京/成都/新加坡/东京")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只发现拓扑，不做端口探测")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "跳过 SSH 公网端口探测")
	return cmd
}

func init() {
	tencentOpsCmd.AddCommand(newPrivateNetworkCommand("private-network", defaultSetupDeps()))
}

func defaultEnsurePrivateNetwork(ctx context.Context, snapshot *setupconfig.Snapshot, opts privatenet.Options, stderr io.Writer) (privatenet.Result, error) {
	if snapshot == nil {
		return privatenet.Result{}, fmt.Errorf("private-network: 缺少初始化配置")
	}
	hosts := privatenet.CollectTencentHosts(snapshot.Manifest)
	if len(hosts) == 0 {
		return privatenet.Result{}, fmt.Errorf("private-network: 没有 provider=tencent 的主机")
	}
	if opts.HomeRegion == "" {
		opts.HomeRegion = snapshot.Manifest.TencentCloud.Region
	}
	options := tencent.ClientOptions{
		SecretID:  snapshot.Manifest.TencentCloud.SecretID,
		SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region:    opts.HomeRegion,
	}
	network, err := tencent.NewNetworkClient(options)
	if err != nil {
		return privatenet.Result{}, fmt.Errorf("private-network: %w", err)
	}
	lighthouse, err := tencent.NewClient(options)
	if err != nil {
		return privatenet.Result{}, fmt.Errorf("private-network: %w", err)
	}
	cloud := privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}
	discoverCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resolved, err := privatenet.ResolveHosts(discoverCtx, cloud, hosts, tencent.ProbeRegions(opts.HomeRegion, opts.ProbeRegions...))
	if err != nil {
		return privatenet.Result{}, err
	}
	result := privatenet.Result{DryRun: opts.DryRun, Status: "ready", Plan: privatenet.BuildPlan(resolved, privatenet.PrivateServicePorts(snapshot.Manifest.EventBus.Port))}
	if opts.DryRun {
		result.Status = "dry_run"
		return result, nil
	}
	if opts.SkipProbe {
		return result, nil
	}
	exec := func(ctx context.Context, publicIP, script string) (string, error) {
		host, findErr := findSetupHostByAddress(snapshot.Manifest, publicIP)
		if findErr != nil {
			return "", findErr
		}
		transport, dialErr := dialSetupHost(ctx, host)
		if dialErr != nil {
			return "", dialErr
		}
		defer transport.Close()
		out, runErr := transport.Run(ctx, []string{"bash", "-lc", script}, nil)
		if runErr != nil {
			return strings.TrimSpace(out.Stdout + "\n" + out.Stderr), runErr
		}
		return out.Stdout, nil
	}
	eventBusPort := ""
	if snapshot.Manifest.EventBus.Port > 0 {
		eventBusPort = fmt.Sprintf("%d", snapshot.Manifest.EventBus.Port)
	}
	result.Probes = privatenet.RunHostProbes(discoverCtx, exec, result.Plan.Hosts, privatenet.PlannedProbes(result.Plan.Hosts, eventBusPort))
	if probeErr := privatenet.PublicProbesFailed(result.Probes); probeErr != nil {
		result.Status = "probe_failed"
		if stderr != nil {
			fmt.Fprintln(stderr, probeErr.Error())
		}
	}
	return result, nil
}

func findSetupHostByAddress(manifest setupconfig.Manifest, address string) (setupconfig.Host, error) {
	want := strings.TrimSpace(address)
	for _, host := range manifest.Hosts() {
		if host.Address == want || host.Host == want {
			return host, nil
		}
	}
	return setupconfig.Host{}, fmt.Errorf("setup_host_not_found: %s", address)
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}
