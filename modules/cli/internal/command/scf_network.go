package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

// resolveSCFRoutePlan discovers the Tencent instance behind the configured
// Storage host and turns it into per-region SCF data-plane routes. Discovery
// is intentionally read-only; applying a VPC binding remains CloudNode's
// responsibility during normal collector publication.
func resolveSCFRoutePlan(ctx context.Context, snapshot *setupconfig.Snapshot, requestedRegion string) (privatenet.SCFRoutePlan, error) {
	if snapshot == nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFFetcher.Enabled {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: scf_fetcher.enabled is false")
	}
	host := snapshot.Manifest.StorageHost
	if !snapshot.Manifest.HasStorageHost() {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: storage_host is required for automatic regional routing")
	}
	if !strings.EqualFold(strings.TrimSpace(host.Provider), "tencent") {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: storage_host provider must be tencent")
	}
	address := strings.TrimSpace(host.Address)
	if address == "" {
		address = strings.TrimSpace(host.Host)
	}
	if address == "" {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: storage_host address is empty")
	}
	if strings.TrimSpace(snapshot.Manifest.TencentCloud.SecretID) == "" || strings.TrimSpace(snapshot.Manifest.TencentCloud.SecretKey) == "" {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: Tencent credentials are required for Storage region discovery")
	}

	network, err := tencent.NewNetworkClient(tencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region: snapshot.Manifest.TencentCloud.Region,
	})
	if err != nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: %w", err)
	}
	lighthouse, err := tencent.NewClient(tencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region: snapshot.Manifest.TencentCloud.Region,
	})
	if err != nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: %w", err)
	}
	cloud := privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}
	regions := tencent.ProbeRegions(snapshot.Manifest.TencentCloud.Region)
	lookupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resolved, err := privatenet.ResolveHosts(lookupCtx, cloud, []privatenet.HostTarget{{
		Name: firstNonEmpty(host.Name, "storage"), Address: address, Provider: "tencent", Roles: []string{"storage"},
	}}, regions)
	if err != nil {
		return privatenet.SCFRoutePlan{}, err
	}
	if len(resolved) != 1 {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: Storage instance discovery returned %d hosts", len(resolved))
	}

	targets := privatenet.CollectSCFTargets(snapshot.Manifest)
	requestedRegion = strings.ToLower(strings.TrimSpace(requestedRegion))
	if requestedRegion != "" {
		seen := false
		for _, target := range targets {
			if strings.EqualFold(target.Region, requestedRegion) {
				seen = true
				break
			}
		}
		if !seen {
			targets = append(targets, privatenet.SCFTarget{Region: requestedRegion})
		}
	}
	routes := make([]privatenet.SCFStorageRoute, 0, len(targets))
	storageRegionConfigured := false
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.Region), strings.TrimSpace(resolved[0].Instance.Region)) {
			storageRegionConfigured = true
		}
		routes = append(routes, privatenet.BuildSCFStorageRoute(resolved[0], target.Region))
	}
	notes := []string{
		"Storage host region/VPC/subnet is discovered from Tencent instead of being hand-copied into each SCF region.",
		"Same-region SCF uses Storage private VPC route; cross-region SCF uses Storage public gateway. CCN is not required.",
		"Private binding assumes the SCF cloud account can use Storage's VPC/subnet; cross-account or insufficient-permission deployments must use the public fallback.",
	}
	if !storageRegionConfigured {
		notes = append(notes, "No enabled SCF region matches Storage's region; add a same-region region or run an explicit one-node canary before the full fleet.")
	}
	return privatenet.SCFRoutePlan{
		Storage: resolved[0], StorageRegionConfigured: storageRegionConfigured, Routes: routes, Notes: notes,
	}, nil
}

func routeMap(plan privatenet.SCFRoutePlan) map[string]privatenet.SCFStorageRoute {
	out := make(map[string]privatenet.SCFStorageRoute, len(plan.Routes))
	for _, route := range plan.Routes {
		out[strings.ToLower(strings.TrimSpace(route.Region))] = route
	}
	return out
}

func newSetupSCFNetworkPlanCommand(deps setupDeps) *cobra.Command {
	var file, region string
	cmd := &cobra.Command{
		Use:   "scf-network-plan",
		Short: "发现 Storage 地域并生成 SCF 私网/公网路由计划",
		Long: `只读查询 moox.toml 中 storage_host 对应的腾讯云实例，输出每个 SCF 地域的访问路径。
同地域且 Storage 有完整 VPC/子网/私网地址时使用私网；跨地域或条件不完整时使用公网。
命令不会创建 CCN、修改主机或修改 SCF。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			plan, err := deps.resolveSCFRoutes(cmd.Context(), snapshot, region)
			if err != nil {
				return err
			}
			if requested := strings.ToLower(strings.TrimSpace(region)); requested != "" {
				filtered := make([]privatenet.SCFStorageRoute, 0, 1)
				for _, route := range plan.Routes {
					if strings.EqualFold(strings.TrimSpace(route.Region), requested) {
						filtered = append(filtered, route)
					}
				}
				plan.Routes = filtered
				plan.StorageRegionConfigured = len(filtered) > 0 && filtered[0].SameRegion
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, plan)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "只输出指定 SCF 地域；为空则输出全部已启用地域")
	return cmd
}
