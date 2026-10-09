package privatenet

import (
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectTencentHostsAndSCFTargets(t *testing.T) {
	manifest := setupconfig.Manifest{
		ControlHost: setupconfig.Host{Name: "control", Address: "106.53.107.122", Provider: "tencent"},
		StorageHost: setupconfig.Host{Name: "storage", Address: "146.56.196.204", Provider: "tencent"},
		ViewHost:    setupconfig.Host{Name: "view", Address: "146.56.196.204", Provider: "tencent"},
		OtherHosts:  []setupconfig.Host{{Name: "compute-1", Address: "43.132.204.177", Provider: "tencent"}},
		SCFFetcher: setupconfig.SCFFetcher{
			Enabled: true,
			Spaces: []setupconfig.SCFFetcherSpace{
				{
					SpaceID: "crypto", FunctionPrefix: "moox-fetcher-crypto-binance", Namespace: "default",
					PublicNetStatus: "ENABLE", RegionBlacklist: []string{"ap-guangzhou"},
					Regions: []setupconfig.SCFFetcherRegion{
						{Region: "ap-singapore", Enabled: true, FunctionCount: 18},
						{Region: "ap-guangzhou", Enabled: true, FunctionCount: 11},
						{Region: "ap-hongkong", Enabled: true, FunctionCount: 40},
						{Region: "ap-shanghai", Enabled: false, FunctionCount: 3},
					},
				},
				{
					SpaceID: "stockcn", FunctionPrefix: "moox-fetcher-stockcn",
					Regions: []setupconfig.SCFFetcherRegion{
						{Region: "ap-chengdu", Enabled: true, FunctionCount: 32},
					},
				},
			},
		},
	}
	hosts := CollectTencentHosts(manifest)
	require.Len(t, hosts, 3)
	scf := CollectSCFTargets(manifest)
	regions := map[string]SCFTarget{}
	for _, item := range scf {
		regions[item.Region] = item
	}
	assert.NotContains(t, regions, "ap-guangzhou", "黑名单地域不发布")
	assert.NotContains(t, regions, "ap-shanghai", "停用的地域不发布")
	assert.Contains(t, regions["ap-hongkong"].Prefixes, "moox-fetcher-crypto-binance")
	assert.Contains(t, regions["ap-chengdu"].Prefixes, "moox-fetcher-stockcn")
	assert.Equal(t, 32, regions["ap-chengdu"].FunctionCount)
	assert.Equal(t, []string{"ap-chengdu", "ap-hongkong", "ap-singapore"}, SCFRegions(scf))
}

func TestCollectSCFTargetsSplitsOverflowNamespaces(t *testing.T) {
	manifest := setupconfig.Manifest{
		SCFFetcher: setupconfig.SCFFetcher{
			Enabled: true,
			TencentLimits: setupconfig.TencentSCFLimits{
				MaxNamespacesPerRegion:   5,
				MaxFunctionsPerNamespace: 50,
			},
			Spaces: []setupconfig.SCFFetcherSpace{{
				SpaceID: "crypto", FunctionPrefix: "moox-fetcher-crypto-binance", Namespace: "moox-crypto",
				Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 54}},
			}},
		},
	}
	targets := CollectSCFTargets(manifest)
	require.Len(t, targets, 2)
	assert.Equal(t, "ap-nanjing", targets[0].Region)
	assert.Equal(t, "moox-crypto", targets[0].Namespace)
	assert.Equal(t, 50, targets[0].FunctionCount)
	assert.Equal(t, "moox-crypto-ns2", targets[1].Namespace)
	assert.Equal(t, 5, targets[1].FunctionCount, "the candidate release canary needs its own private-network slot")
}

func TestBuildPlanSummarizesHostsWithoutSCFMutation(t *testing.T) {
	plan := BuildPlan([]ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", InstanceID: "lhins-1", PrivateIPs: []string{"10.0.1.12"}}, CidrBlock: "10.0.0.0/16"},
		{HostTarget: HostTarget{Name: "compute-1", Address: "43.132.204.177", Roles: []string{"compute-1"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", InstanceID: "ins-1", VpcID: "vpc-hk", SubnetID: "subnet-hk", PrivateIPs: []string{"172.19.32.13"}}, CidrBlock: "172.19.0.0/16"},
	}, []string{"11003"})
	require.Equal(t, []string{"10.0.0.0/16", "172.19.0.0/16"}, plan.CIDRs)
	require.Equal(t, []string{"10.0.0.0/16"}, plan.CIDRsByArea["mainland"])
	require.Equal(t, []string{"172.19.0.0/16"}, plan.CIDRsByArea["overseas"])
	require.Len(t, plan.Recommended.Hosts, 2)
	require.NotEmpty(t, plan.Recommended.Notes)
}

// productionDirectory 是生产三台主机的服务目录：storage（南京）和 compute-1（香港）各有一份外部接入。
func productionDirectory(t *testing.T) servicecatalog.Directory {
	t.Helper()
	deployment := servicecatalog.Deployment{Hosts: []servicecatalog.Host{
		{ID: "control", Address: "106.53.107.122", Enabled: true},
		{ID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing", Enabled: true},
		{ID: "compute-1", Address: "43.132.204.177", PrivateAddress: "172.19.32.13", Region: "ap-hongkong", Enabled: true},
	}}
	for host, components := range map[string][]string{
		"control":   {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
		"storage":   {"storage-primary", "storage-node", "storage-view", "access"},
		"compute-1": {"trade", "access"},
	} {
		for _, component := range components {
			deployment.Placements = append(deployment.Placements, servicecatalog.Placement{HostID: host, ComponentID: component, Enabled: true})
		}
	}
	compiled, err := servicecatalog.Default().Compile(deployment)
	require.NoError(t, err)
	return compiled.Directory
}

func TestPrivateAccessHostsListsOnlySameRegionAccessHosts(t *testing.T) {
	hosts, err := PrivateAccessHosts(productionDirectory(t), []string{"ap-hongkong", "ap-singapore", "ap-nanjing", "ap-hongkong"})
	require.NoError(t, err)
	require.Equal(t, []HostTarget{
		{Name: "compute-1", Address: "43.132.204.177", Roles: []string{"access"}, Provider: "tencent"},
		{Name: "storage", Address: "146.56.196.204", Roles: []string{"access"}, Provider: "tencent"},
	}, hosts)
}

func TestBuildSCFAccessRouteSelectsVPCForSameRegionAccess(t *testing.T) {
	directory := productionDirectory(t)
	hosts := map[string]ResolvedHost{
		"compute-1": {Instance: tencent.CloudInstance{Region: "ap-hongkong", VpcID: "vpc-hk", SubnetID: "subnet-hk", PrivateIPs: []string{"172.19.32.13"}}},
	}
	route, err := BuildSCFAccessRoute(directory, hosts, "AP-HONGKONG")
	require.NoError(t, err)
	assert.Equal(t, SCFAccessRoute{
		Region: "ap-hongkong", Network: "vpc", AccessHostID: "compute-1", AccessID: "access@compute-1", AccessAddress: "172.19.32.13:11004",
		VpcID: "vpc-hk", SubnetID: "subnet-hk", Reason: route.Reason,
	}, route)

	route, err = BuildSCFAccessRoute(directory, nil, "ap-singapore")
	require.NoError(t, err)
	assert.Equal(t, "public", route.Network)
	assert.Equal(t, "access@storage", route.AccessID)
	assert.Equal(t, "146.56.196.204:11004", route.AccessAddress)
	assert.Empty(t, route.VpcID)
}

func TestBuildSCFAccessRouteFailsClosedWithoutVPC(t *testing.T) {
	directory := productionDirectory(t)
	_, err := BuildSCFAccessRoute(directory, nil, "ap-nanjing")
	require.ErrorContains(t, err, "缺少外部接入主机 storage 的实例信息")

	_, err = BuildSCFAccessRoute(directory, map[string]ResolvedHost{
		"storage": {Instance: tencent.CloudInstance{Region: "ap-nanjing", PrivateIPs: []string{"10.206.0.5"}}},
	}, "ap-nanjing")
	require.ErrorContains(t, err, "没有完整的 VPC 和子网信息")

	_, err = BuildSCFAccessRoute(directory, map[string]ResolvedHost{
		"storage": {Instance: tencent.CloudInstance{Region: "ap-nanjing", VpcID: "vpc-nj", SubnetID: "subnet-nj", PrivateIPs: []string{"10.206.0.9"}}},
	}, "ap-nanjing")
	require.ErrorContains(t, err, "不一致", "登记的私网地址与实例不一致时不能绑定")
}

func TestSCFRoutePlanRouteLookup(t *testing.T) {
	plan := SCFRoutePlan{Routes: []SCFAccessRoute{{Region: "ap-nanjing", Network: "vpc"}}}
	route, ok := plan.Route(" AP-NANJING ")
	require.True(t, ok)
	assert.Equal(t, "vpc", route.Network)
	_, ok = plan.Route("ap-hongkong")
	assert.False(t, ok)
}

func TestPlannedProbesUsePublicIPs(t *testing.T) {
	hosts := []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{PrivateIPs: []string{"10.1.12.13"}}, Area: "mainland"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{PrivateIPs: []string{"10.206.0.5"}}, Area: "mainland"},
		{HostTarget: HostTarget{Name: "compute-1", Address: "43.132.204.177", Roles: []string{"compute-1"}}, Instance: tencent.CloudInstance{PrivateIPs: []string{"172.19.32.13"}}, Area: "overseas"},
	}
	probes := PlannedProbes(hosts, "4222")
	require.NotEmpty(t, probes)
	var controlToStorage, computeToStorage Probe
	for _, probe := range probes {
		if probe.From == "control" && probe.To == "storage" && probe.Port == "11003" {
			controlToStorage = probe
		}
		if probe.From == "compute-1" && probe.To == "storage" && probe.Port == "11003" {
			computeToStorage = probe
		}
	}
	assert.Equal(t, "open", controlToStorage.Expect)
	assert.Equal(t, "146.56.196.204", controlToStorage.PublicIP)
	assert.Equal(t, "open", computeToStorage.Expect)
	assert.Equal(t, "146.56.196.204", computeToStorage.PublicIP)
	assert.NoError(t, PublicProbesFailed([]Probe{{Expect: "open", Status: "open"}}))
	assert.Error(t, PublicProbesFailed([]Probe{{From: "control", To: "storage", Port: "11003", Expect: "open", Status: "closed"}}))
	assert.Equal(t, "4222", PrivateServicePorts(4222)[0])
}
