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
	var dryRun, skipSCF, skipHosts, skipProbe, probeOnly, rewriteRuntime, updateGateway, restorePublic bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "腾讯云主机与 SCF 一律走公网；不再创建云联网",
		Long: `读取 moox.toml 中 provider=tencent 的主机。容器与主机之间一律使用公网 IP，
不再创建云联网、不再给 SCF 绑定 VPC。

可用 --restore-scf-public 把存量函数网关改回公网并解绑 VPC。
可用 --rewrite-runtime 把主机 runtime.env 中的 Storage RPC 改回公网 IP。

SSH 和控制台入口继续使用公网 IP。不会调用 ModifyInstancesVpcAttribute。

示例：
  moox-cli setup private-network --file ./moox.toml --dry-run
  moox-cli setup private-network --file ./moox.toml --restore-scf-public --dry-run
  moox-cli setup private-network --file ./moox.toml --restore-scf-public
  moox-cli setup private-network --file ./moox.toml --rewrite-runtime --skip-probe`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if restorePublic || updateGateway {
				restorePublic = true
				skipSCF = false
				skipHosts = true
				skipProbe = true
			}
			opts := privatenet.Options{
				HomeRegion:       snapshot.Manifest.TencentCloud.Region,
				CCNName:          strings.TrimSpace(ccnName),
				DryRun:           dryRun,
				SkipSCF:          skipSCF,
				SkipHosts:        skipHosts,
				SkipProbe:        skipProbe,
				ProbeOnly:        probeOnly,
				RewriteRuntime:   rewriteRuntime,
				RestoreSCFPublic: restorePublic,
				UnbindSCFVPC:     restorePublic,
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
	cmd.Flags().BoolVar(&skipSCF, "skip-scf", true, "不给 SCF 建 VPC/绑 CCN；SCF 统一走公网")
	cmd.Flags().BoolVar(&skipHosts, "skip-hosts", true, "跳过主机安全组/轻量防火墙")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "跳过 SSH 公网端口探测")
	cmd.Flags().BoolVar(&probeOnly, "probe-only", false, "只探测公网连通性，不调用写 API")
	cmd.Flags().BoolVar(&rewriteRuntime, "rewrite-runtime", false, "把主机 runtime.env 中的 Storage RPC 改回公网 IP 并重启 Collector")
	cmd.Flags().BoolVar(&updateGateway, "update-scf-gateway", false, "已废弃，等价于 --restore-scf-public")
	cmd.Flags().BoolVar(&restorePublic, "restore-scf-public", false, "把 SCF Storage 网关改回公网 IP，并解除函数 VPC/CCN")
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
	if opts.RestoreSCFPublic {
		scf = privatenet.CollectSCFRestoreTargets(snapshot.Manifest)
	} else if opts.SkipSCF {
		scf = nil
	}
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
	if opts.DryRun || (!shouldRunPrivateNetworkProbes(opts) && !shouldRewritePublicStorageRPC(opts)) {
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
	if shouldRunPrivateNetworkProbes(opts) {
		eventBusPort := ""
		if snapshot.Manifest.EventBus.Port > 0 {
			eventBusPort = fmt.Sprintf("%d", snapshot.Manifest.EventBus.Port)
		}
		probes := privatenet.PlannedProbes(result.Plan.Hosts, eventBusPort)
		result.Probes = privatenet.RunHostProbes(applyCtx, exec, result.Plan.Hosts, probes)
		result.RuntimeConfigHits = privatenet.InspectPublicIPRefs(applyCtx, exec, result.Plan.Hosts)
		if probeErr := privatenet.PublicProbesFailed(result.Probes); probeErr != nil {
			result.Status = "probe_failed"
			if stderr != nil {
				fmt.Fprintln(stderr, probeErr.Error())
			}
			if !shouldRewritePublicStorageRPC(opts) {
				return result, nil
			}
		}
	}
	if shouldRewritePublicStorageRPC(opts) {
		result.Plan.Hosts = appendMissingFactorHosts(result.Plan.Hosts, snapshot)
		if err := rewriteMainlandStorageRPC(applyCtx, exec, result, stderr); err != nil {
			result.Status = "rewrite_failed"
			return result, err
		}
		result.Status = "runtime_rewritten"
	}
	return result, nil
}

func shouldRunPrivateNetworkProbes(opts privatenet.Options) bool {
	return !opts.DryRun && !opts.SkipProbe
}

func shouldRewritePublicStorageRPC(opts privatenet.Options) bool {
	return opts.RewriteRuntime && !opts.DryRun
}

func isFactorRewriteHost(host privatenet.ResolvedHost) bool {
	if strings.Contains(strings.ToLower(host.Name), "factor") {
		return true
	}
	for _, role := range host.Roles {
		if strings.Contains(strings.ToLower(role), "factor") {
			return true
		}
	}
	return false
}

func appendMissingFactorHosts(hosts []privatenet.ResolvedHost, snapshot *setupconfig.Snapshot) []privatenet.ResolvedHost {
	if snapshot == nil {
		return hosts
	}
	seen := map[string]struct{}{}
	for _, host := range hosts {
		if address := strings.TrimSpace(host.Address); address != "" {
			seen[address] = struct{}{}
		}
	}
	for _, host := range snapshot.Manifest.Hosts() {
		if !strings.Contains(strings.ToLower(strings.TrimSpace(host.Name)), "factor") {
			continue
		}
		address := strings.TrimSpace(host.Address)
		if address == "" {
			address = strings.TrimSpace(host.Host)
		}
		if address == "" {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		hosts = append(hosts, privatenet.ResolvedHost{
			HostTarget: privatenet.HostTarget{Name: host.Name, Address: address, Roles: []string{host.Name}},
		})
	}
	return hosts
}

func rewriteMainlandStorageRPC(ctx context.Context, exec func(context.Context, string, string) (string, error), result privatenet.Result, stderr io.Writer) error {
	publicIP := strings.TrimSpace(result.RecommendedConfig.StoragePublicIP)
	privateIP := strings.TrimSpace(result.RecommendedConfig.StoragePrivateIP)
	if publicIP == "" || privateIP == "" {
		return fmt.Errorf("private-network: storage public/private ip missing")
	}
	rewrote := false
	for _, host := range result.Plan.Hosts {
		if privatenetHostHasRole(host, "control") {
			if err := rewriteControlCollectorStorageRPC(ctx, exec, host.Address, privateIP, publicIP, stderr); err != nil {
				return err
			}
			rewrote = true
		}
		if isFactorRewriteHost(host) {
			if err := rewriteFactorEngineStorageRPC(ctx, exec, host.Address, privateIP, publicIP, stderr); err != nil {
				return err
			}
			rewrote = true
		}
	}
	if !rewrote {
		return fmt.Errorf("private-network: no control or factor host to rewrite")
	}
	return nil
}

func rewriteControlCollectorStorageRPC(ctx context.Context, exec func(context.Context, string, string) (string, error), controlIP, privateIP, publicIP string, stderr io.Writer) error {
	script := fmt.Sprintf(`set -euo pipefail
envfile=/data/moox/prod/config/runtime.env
test -f "$envfile"
cp -a "$envfile" "$envfile.pre-public-net"
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
`, privateIP, publicIP)
	if stderr != nil {
		fmt.Fprintf(stderr, "rewrite control storage rpc %s -> %s and restart collector\n", privateIP, publicIP)
	}
	stdout, err := exec(ctx, controlIP, script)
	if stderr != nil && strings.TrimSpace(stdout) != "" {
		fmt.Fprintln(stderr, strings.TrimSpace(stdout))
	}
	if err != nil {
		return fmt.Errorf("rewrite collector runtime: %w", err)
	}
	if !strings.Contains(stdout, "ip://"+publicIP+":11003") {
		return fmt.Errorf("collector did not pick up public storage rpc target")
	}
	return nil
}

func rewriteFactorEngineStorageRPC(ctx context.Context, exec func(context.Context, string, string) (string, error), hostIP, privateIP, publicIP string, stderr io.Writer) error {
	script := fmt.Sprintf(`set -euo pipefail
rewritten=0
for envfile in "$HOME/.config/moox/factor-engine/runtime.env" "$HOME/moox/factor-engine/config/runtime.env"; do
  if test -f "$envfile"; then
    cp -a "$envfile" "$envfile.pre-public-net"
    python3 -c 'import pathlib,sys
p=pathlib.Path(sys.argv[3])
text=p.read_text()
old="ip://"+sys.argv[1]+":11003"
new="ip://"+sys.argv[2]+":11003"
if old in text:
    p.write_text(text.replace(old,new,1))
    print("rewrote "+old+" -> "+new+" in "+str(p))
elif new in text:
    print("already "+new+" in "+str(p))
else:
    raise SystemExit("storage rpc target "+old+" not found in "+str(p))
' %s %s "$envfile"
    rewritten=1
  fi
done
test "$rewritten" = 1
start=""
for candidate in "$HOME/moox/factor-engine/start.sh"; do
  if test -x "$candidate"; then
    start="$candidate"
    break
  fi
done
test -n "$start"
"$start"
pid=$(cat "$(dirname "$start")/run/factor-engine.pid")
tr "\0" "\n" < /proc/$pid/environ | grep "^MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=" || true
`, privateIP, publicIP)
	if stderr != nil {
		fmt.Fprintf(stderr, "rewrite factor-engine storage rpc %s -> %s on %s\n", privateIP, publicIP, hostIP)
	}
	stdout, err := exec(ctx, hostIP, script)
	if stderr != nil && strings.TrimSpace(stdout) != "" {
		fmt.Fprintln(stderr, strings.TrimSpace(stdout))
	}
	if err != nil {
		return fmt.Errorf("rewrite factor-engine runtime: %w", err)
	}
	if !strings.Contains(stdout, "ip://"+publicIP+":11003") {
		return fmt.Errorf("factor-engine did not pick up public storage rpc target")
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
