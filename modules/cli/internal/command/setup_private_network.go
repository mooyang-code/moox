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
	var file, ccnName, probeRegions string
	var dryRun, skipSCF, skipHosts, skipProbe, probeOnly, rewriteRuntime, updateGateway bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "打通腾讯云主机与 SCF 的内网",
		Long: `读取 moox.toml 中 provider=tencent 的主机，查询其 VPC/子网/内网 IP，
必要时创建云联网并绑定各地域 SCF VPC，使 Control、Storage、Compute 和 SCF 走内网通信。

不会调用 ModifyInstancesVpcAttribute，因此不会停机迁移现有 CVM。
SSH 和控制台入口继续使用公网 IP。

示例：
  moox-cli setup private-network --file ./moox.toml --dry-run
  moox-cli setup private-network --file ./moox.toml
  moox-cli ops tencent private-network --file ./moox.toml --skip-scf
  moox-cli setup private-network --file ./moox.toml --ccn-name moox-private-network`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			opts := privatenet.Options{
				HomeRegion:       snapshot.Manifest.TencentCloud.Region,
				CCNName:          strings.TrimSpace(ccnName),
				DryRun:           dryRun,
				SkipSCF:          skipSCF,
				SkipHosts:        skipHosts,
				SkipProbe:        skipProbe,
				ProbeOnly:        probeOnly,
				RewriteRuntime:   rewriteRuntime,
				UpdateSCFGateway: updateGateway,
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
	cmd.Flags().StringVar(&ccnName, "ccn-name", privatenet.DefaultCCNName, "云联网名称，已存在则复用")
	cmd.Flags().StringVar(&probeRegions, "probe-regions", "", "额外探测地域，逗号分隔；默认含广州/香港/上海/北京/成都/新加坡/东京")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只发现拓扑并输出计划，不调用写 API")
	cmd.Flags().BoolVar(&skipSCF, "skip-scf", false, "只处理 CVM/轻量主机，不绑定 SCF VPC")
	cmd.Flags().BoolVar(&skipHosts, "skip-hosts", false, "跳过主机安全组/轻量防火墙（仍会解析主机内网 IP）")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "跳过 SSH 内网端口探测")
	cmd.Flags().BoolVar(&probeOnly, "probe-only", false, "只探测内网连通性和运行时公网 IP 引用，不调用写 API")
	cmd.Flags().BoolVar(&rewriteRuntime, "rewrite-runtime", false, "把国内主机 runtime.env 中的 Storage RPC 公网 IP 改成内网 IP 并重启 Collector")
	cmd.Flags().BoolVar(&updateGateway, "update-scf-gateway", false, "把已绑定 VPC 的函数环境变量中的 Storage 公网 IP 替换为内网 IP")
	return cmd
}

func init() {
	tencentOpsCmd.AddCommand(newPrivateNetworkCommand("private-network", defaultSetupDeps()))
}

func defaultEnsurePrivateNetwork(ctx context.Context, snapshot *setupconfig.Snapshot, opts privatenet.Options, stderr io.Writer) (privatenet.Result, error) {
	if snapshot == nil {
		return privatenet.Result{}, fmt.Errorf("private-network: setup configuration is missing")
	}
	hosts := privatenet.CollectTencentHosts(snapshot.Manifest)
	if len(hosts) == 0 {
		return privatenet.Result{}, fmt.Errorf("private-network: no hosts with provider=tencent")
	}
	scf := privatenet.CollectSCFTargets(snapshot.Manifest)
	if opts.HomeRegion == "" {
		opts.HomeRegion = snapshot.Manifest.TencentCloud.Region
	}
	if opts.CCNName == "" {
		opts.CCNName = privatenet.DefaultCCNName
	}
	network, err := tencent.NewNetworkClient(tencent.ClientOptions{
		SecretID:  snapshot.Manifest.TencentCloud.SecretID,
		SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region:    opts.HomeRegion,
	})
	if err != nil {
		return privatenet.Result{}, fmt.Errorf("private-network: %w", err)
	}
	lighthouse, err := tencent.NewClient(tencent.ClientOptions{
		SecretID:  snapshot.Manifest.TencentCloud.SecretID,
		SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region:    opts.HomeRegion,
	})
	if err != nil {
		return privatenet.Result{}, fmt.Errorf("private-network: %w", err)
	}
	cloud := privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}
	regions := tencent.ProbeRegions(opts.HomeRegion, opts.ProbeRegions...)
	applyCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		applyCtx, cancel = context.WithTimeout(ctx, 45*time.Minute)
		defer cancel()
	}
	resolved, err := privatenet.ResolveHosts(applyCtx, cloud, hosts, regions)
	if err != nil {
		return privatenet.Result{}, err
	}
	if opts.SkipSCF {
		scf = nil
	}
	plan := privatenet.BuildPlan(opts, resolved, scf, privatenet.PrivateServicePorts(snapshot.Manifest.EventBus.Port))
	result := privatenet.Result{Plan: plan, RecommendedConfig: plan.Recommended, Status: "ready"}
	if !opts.ProbeOnly {
		applied, err := privatenet.Apply(applyCtx, cloud, opts, plan, stderr)
		if err != nil {
			return applied, err
		}
		result = applied
	} else {
		result.DryRun = false
		result.Status = "probe_only"
	}
	if opts.DryRun || opts.SkipProbe {
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
	probes := privatenet.PlannedProbes(result.Plan.Hosts, eventBusPort)
	result.Probes = privatenet.RunHostProbes(applyCtx, exec, result.Plan.Hosts, probes)
	result.RuntimeConfigHits = privatenet.InspectPublicIPRefs(applyCtx, exec, result.Plan.Hosts)
	if probeErr := privatenet.MainlandProbesFailed(result.Probes); probeErr != nil {
		result.Status = "probe_failed"
		if stderr != nil {
			fmt.Fprintln(stderr, probeErr.Error())
		}
		return result, nil
	}
	if opts.RewriteRuntime {
		if err := rewriteMainlandStorageRPC(applyCtx, exec, result, stderr); err != nil {
			result.Status = "rewrite_failed"
			return result, err
		}
		result.Status = "runtime_rewritten"
	}
	return result, nil
}

func rewriteMainlandStorageRPC(ctx context.Context, exec func(context.Context, string, string) (string, error), result privatenet.Result, stderr io.Writer) error {
	publicIP := strings.TrimSpace(result.RecommendedConfig.StoragePublicIP)
	privateIP := strings.TrimSpace(result.RecommendedConfig.StoragePrivateIP)
	if publicIP == "" || privateIP == "" {
		return fmt.Errorf("private-network: storage public/private ip missing")
	}
	controlIP := ""
	for _, host := range result.Plan.Hosts {
		if privatenetHostHasRole(host, "control") {
			controlIP = host.Address
			break
		}
	}
	if controlIP == "" {
		return fmt.Errorf("private-network: control host missing")
	}
	script := fmt.Sprintf(`set -euo pipefail
envfile=/data/moox/prod/config/runtime.env
test -f "$envfile"
cp -a "$envfile" "$envfile.pre-private-net"
python3 -c 'import pathlib,sys
p=pathlib.Path("/data/moox/prod/config/runtime.env")
text=p.read_text()
old="ip://"+sys.argv[1]+":11003"
new="ip://"+sys.argv[2]+":11003"
if old in text:
    p.write_text(text.replace(old,new,1))
    print("rewrote "+old+" -> "+new)
elif new in text:
    print("already "+new)
else:
    raise SystemExit("storage rpc target "+old+" not found")
' %s %s
cd /data/moox/prod
./start.sh collector
pid=$(cat /data/moox/prod/run/collector.pid)
tr "\0" "\n" < /proc/$pid/environ | grep "^MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET=" || true
`, publicIP, privateIP)
	if stderr != nil {
		fmt.Fprintf(stderr, "rewrite control storage rpc %s -> %s and restart collector\n", publicIP, privateIP)
	}
	stdout, err := exec(ctx, controlIP, script)
	if stderr != nil && strings.TrimSpace(stdout) != "" {
		fmt.Fprintln(stderr, strings.TrimSpace(stdout))
	}
	if err != nil {
		return fmt.Errorf("rewrite runtime: %w", err)
	}
	if !strings.Contains(stdout, "ip://"+privateIP+":11003") {
		return fmt.Errorf("collector did not pick up private storage rpc target")
	}
	return nil
}

func privatenetHostHasRole(host privatenet.ResolvedHost, role string) bool {
	for _, item := range host.Roles {
		if item == role {
			return true
		}
	}
	return false
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
