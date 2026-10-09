package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// resolveSCFRoutePlan 生成各地域 SCF 采集函数访问外部接入的路由：外部接入的位置取自控制面的服务目录，走私网的
// 外部接入主机再向腾讯云查询 VPC 和子网。只读；VPC 绑定在发布采集函数时由 CloudNode 完成。requestedRegion 非空时
// 只计算该地域。
func resolveSCFRoutePlan(ctx context.Context, snapshot *setupconfig.Snapshot, requestedRegion string) (privatenet.SCFRoutePlan, error) {
	if snapshot == nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: 缺少初始化配置")
	}
	if !snapshot.Manifest.SCFFetcher.Enabled {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: scf_fetcher.enabled 为 false")
	}
	regions := privatenet.SCFRegions(privatenet.CollectSCFTargets(snapshot.Manifest))
	if requested := strings.ToLower(strings.TrimSpace(requestedRegion)); requested != "" {
		regions = []string{requested}
	}
	gateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		return privatenet.SCFRoutePlan{}, err
	}
	defer gateway.Close()
	directoryCtx, cancelDirectory := context.WithTimeout(ctx, 30*time.Second)
	view, err := gateway.Directory(directoryCtx)
	cancelDirectory()
	if err != nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: 读取控制面的服务目录: %w", err)
	}
	privateHosts, err := privatenet.PrivateAccessHosts(view.Directory, regions)
	if err != nil {
		return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: %w", err)
	}
	resolved := map[string]privatenet.ResolvedHost{}
	if len(privateHosts) > 0 {
		hosts, err := resolveTencentHosts(ctx, snapshot, privateHosts)
		if err != nil {
			return privatenet.SCFRoutePlan{}, err
		}
		for _, host := range hosts {
			resolved[host.Name] = host
		}
	}
	plan := privatenet.SCFRoutePlan{Notes: []string{
		"外部接入的位置取自控制面的服务目录；同地域主机上有外部接入时，函数绑定该主机所在的 VPC 走私网，否则走 access@storage 的公网地址。",
		"Collector 按同一规则维护函数环境变量中的外部接入地址；不需要云联网（CCN）。",
	}}
	for _, region := range regions {
		route, err := privatenet.BuildSCFAccessRoute(view.Directory, resolved, region)
		if err != nil {
			return privatenet.SCFRoutePlan{}, fmt.Errorf("scf-network: %w", err)
		}
		plan.Routes = append(plan.Routes, route)
	}
	if host, ok := view.Directory.Host(servicecatalog.AccessFallbackHostID); ok {
		plan.PreferredRegion = strings.ToLower(strings.TrimSpace(host.Region))
	}
	return plan, nil
}

// resolveTencentHosts 向腾讯云查询主机对应的实例。
func resolveTencentHosts(ctx context.Context, snapshot *setupconfig.Snapshot, hosts []privatenet.HostTarget) ([]privatenet.ResolvedHost, error) {
	tencentCloud := snapshot.Manifest.TencentCloud
	if strings.TrimSpace(tencentCloud.SecretID) == "" || strings.TrimSpace(tencentCloud.SecretKey) == "" {
		return nil, fmt.Errorf("scf-network: 查询外部接入主机的 VPC 需要腾讯云凭据")
	}
	options := tencent.ClientOptions{SecretID: tencentCloud.SecretID, SecretKey: tencentCloud.SecretKey, Region: tencentCloud.Region}
	network, err := tencent.NewNetworkClient(options)
	if err != nil {
		return nil, fmt.Errorf("scf-network: %w", err)
	}
	lighthouse, err := tencent.NewClient(options)
	if err != nil {
		return nil, fmt.Errorf("scf-network: %w", err)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return privatenet.ResolveHosts(lookupCtx, privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}, hosts, tencent.ProbeRegions(tencentCloud.Region))
}

func newSetupSCFNetworkPlanCommand(deps setupDeps) *cobra.Command {
	var file, region string
	cmd := &cobra.Command{
		Use:   "scf-network-plan",
		Short: "生成 SCF 采集函数访问外部接入的私网/公网路由计划",
		Long: `只读：从控制面的服务目录取外部接入的位置，输出每个 SCF 地域的访问路径。
同地域主机上有外部接入时绑定该主机所在的 VPC 走私网；否则走 access@storage 的公网地址。
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
