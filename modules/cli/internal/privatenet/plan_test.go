package privatenet

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
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
					},
				},
				{
					SpaceID: "stockcn", FunctionPrefix: "moox-fetcher-stockcn",
					InstrumentSnapshotFunctionPrefix: "moox-fetcher-stockcn-instrument",
					InstrumentSnapshotRegion:         "ap-chengdu",
					Regions: []setupconfig.SCFFetcherRegion{
						{Region: "ap-chengdu", Enabled: true, FunctionCount: 32},
					},
				},
			},
		},
	}
	hosts := CollectTencentHosts(manifest)
	require.Len(t, hosts, 3)
	assert.Equal(t, "146.56.196.204", StoragePublicIP(hosts))
	scf := CollectSCFTargets(manifest)
	regions := map[string]SCFTarget{}
	for _, item := range scf {
		regions[item.Region] = item
	}
	_, hasGZ := regions["ap-guangzhou"]
	assert.False(t, hasGZ)
	assert.Contains(t, regions["ap-hongkong"].Prefixes, "moox-fetcher-crypto-binance")
	assert.Contains(t, regions["ap-chengdu"].Prefixes, "moox-fetcher-stockcn-instrument")
	assert.Equal(t, 33, regions["ap-chengdu"].FunctionCount)
}

func TestBuildPlanDoesNotCreatePrivateNetwork(t *testing.T) {
	plan := BuildPlan(Options{CCNName: DefaultCCNName}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", InstanceID: "lhins-1", PrivateIPs: []string{"10.0.1.12"}}, CidrBlock: "10.0.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", InstanceID: "ins-1", VpcID: "vpc-storage", SubnetID: "subnet-storage", PrivateIPs: []string{"10.1.2.8"}}, CidrBlock: "10.1.0.0/16"},
	}, []SCFTarget{
		{Region: "ap-hongkong", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"},
		{Region: "ap-singapore", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"},
	}, []string{"11003"})
	require.False(t, plan.NeedCCN)
	require.False(t, plan.NeedOverseasCCN)
	require.False(t, plan.NeedMainlandCCN)
	require.Empty(t, plan.VPCs)
	require.Empty(t, plan.SCF)
	require.Equal(t, "146.56.196.204", plan.Recommended.StoragePublicIP)
	require.Equal(t, "ip://146.56.196.204:11003", plan.Recommended.SCFGatewayTarget)
	require.Contains(t, strings.Join(plan.Recommended.Notes, "\n"), "公网")
}

func TestBuildPlanRestoreKeepsSCFWithoutVPC(t *testing.T) {
	plan := BuildPlan(Options{RestoreSCFPublic: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", PrivateIPs: []string{"10.1.12.13"}}, CidrBlock: "10.1.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", VpcID: "vpc-storage", SubnetID: "subnet-storage", PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{
		{Region: "ap-guangzhou", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}},
		{Region: "ap-singapore", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}},
	}, []string{"11003"})
	require.False(t, plan.NeedCCN)
	require.Empty(t, plan.VPCs)
	assert.Equal(t, "mainland", plan.Recommended.StorageArea)
	require.Len(t, plan.SCF, 2)
	for _, bind := range plan.SCF {
		assert.Empty(t, bind.VpcID)
		assert.Empty(t, bind.SubnetID)
	}
}

func TestApplyDryRunRestoreListsFunctions(t *testing.T) {
	fake := &fakeCloud{
		functions: []tencent.SCFFunction{
			{Region: "ap-shanghai", Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0"},
			{Region: "ap-shanghai", Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-1"},
		},
	}
	plan := BuildPlan(Options{RestoreSCFPublic: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{{Region: "ap-shanghai", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{DryRun: true, RestoreSCFPublic: true, HomeRegion: "ap-guangzhou"}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "dry_run", result.Status)
	assert.Zero(t, fake.updateSCF)
	assert.Zero(t, fake.detachVPC)
	require.Len(t, result.Actions, 2)
	assert.Equal(t, "scf-restore-public", result.Actions[1].Kind)
	assert.Contains(t, result.Actions[1].Detail, "functions=2")
}

func TestApplyDryRunDoesNotMutateCloud(t *testing.T) {
	fake := &fakeCloud{}
	plan := BuildPlan(Options{}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "1.1.1.1", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", VpcID: "vpc-1"}},
	}, nil, []string{"11003"})
	var stderr bytes.Buffer
	result, err := Apply(context.Background(), fake, Options{DryRun: true, HomeRegion: "ap-guangzhou"}, plan, &stderr)
	require.NoError(t, err)
	assert.Equal(t, "dry_run", result.Status)
	assert.Zero(t, fake.ensureCCN)
	assert.Zero(t, fake.updateSCF)
}

func TestApplyDoesNotCreateCCN(t *testing.T) {
	fake := &fakeCloud{}
	plan := BuildPlan(Options{}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", PrivateIPs: []string{"10.1.12.13"}}, CidrBlock: "10.1.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", VpcID: "vpc-storage", SecurityGroupIDs: []string{"sg-1"}, PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, nil, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou"}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "public_only", result.Status)
	assert.Zero(t, fake.ensureCCN)
	assert.Zero(t, fake.attachVPC)
	assert.Zero(t, fake.acceptCCN)
	assert.Zero(t, fake.updateSCF)
}

func TestApplyRetriesSCFUpdating(t *testing.T) {
	fake := &fakeCloud{
		updateErrs: 1,
		functions:  []tencent.SCFFunction{{Region: "ap-shanghai", Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0"}},
		function: tencent.SCFFunction{
			Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0",
			VpcID: "vpc-scf", SubnetID: "subnet-scf",
			Environment: map[string]string{"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://10.206.0.5:11003"},
		},
	}
	plan := BuildPlan(Options{RestoreSCFPublic: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", InstanceID: "ins-1", VpcID: "vpc-storage", SecurityGroupIDs: []string{"sg-1"}, PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{{Region: "ap-shanghai", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou", RestoreSCFPublic: true, UnbindSCFVPC: true}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SCFUpdated)
	assert.Equal(t, 2, fake.updateSCF)
	assert.Equal(t, "ip://146.56.196.204:11003", fake.lastEnv["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
	assert.Empty(t, fake.lastVPC)
}

func TestIsRetryableSCFUpdate(t *testing.T) {
	assert.True(t, isRetryableSCFUpdate(fmt.Errorf("FailedOperation.UpdateFunctionConfiguration: 当前函数处于Updating状态，无法进行此操作，请稍后重试。")))
	assert.False(t, isRetryableSCFUpdate(fmt.Errorf("InvalidParameterValue: FunctionName")))
}

func TestApplyRestoresPublicGatewayAndUnbindsVPC(t *testing.T) {
	fake := &fakeCloud{
		functions: []tencent.SCFFunction{{Region: "ap-shanghai", Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0"}},
		function: tencent.SCFFunction{
			Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0",
			VpcID: "vpc-scf", SubnetID: "subnet-scf", Region: "ap-shanghai",
			Environment: map[string]string{"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://10.206.0.5:11003"},
		},
	}
	plan := BuildPlan(Options{RestoreSCFPublic: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", InstanceID: "ins-1", VpcID: "vpc-storage", PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{{Region: "ap-shanghai", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	require.Empty(t, plan.VPCs)
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou", RestoreSCFPublic: true, UnbindSCFVPC: true}, plan, nil)
	require.NoError(t, err)
	assert.Zero(t, fake.ensureCCN)
	assert.Equal(t, "scf_public_restored", result.Status)
	assert.Equal(t, 1, result.SCFUpdated)
	assert.Equal(t, "ip://146.56.196.204:11003", fake.lastEnv["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
	assert.Empty(t, fake.lastVPC)
	assert.Empty(t, fake.lastSubnet)
	assert.Equal(t, 1, fake.detachVPC)
	require.NotEmpty(t, result.Actions)
	assert.Equal(t, "scf-restore-public", result.Actions[0].Kind)
	assert.Equal(t, "ccn-detach", result.Actions[len(result.Actions)-1].Kind)
}

func TestApplyRestoreDoesNotDetachHostVPC(t *testing.T) {
	fake := &fakeCloud{
		functions: []tencent.SCFFunction{{Region: "ap-hongkong", Namespace: "default", FunctionName: "moox-fetcher-crypto-binance-ap-hongkong-0"}},
		function: tencent.SCFFunction{
			Namespace: "default", FunctionName: "moox-fetcher-crypto-binance-ap-hongkong-0",
			VpcID: "vpc-compute", SubnetID: "subnet-compute", Region: "ap-hongkong",
			Environment: map[string]string{"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://10.206.0.5:11003"},
		},
	}
	plan := BuildPlan(Options{RestoreSCFPublic: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", InstanceID: "ins-storage", VpcID: "vpc-storage", PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
		{HostTarget: HostTarget{Name: "compute-1", Address: "43.132.204.177", Roles: []string{"compute-1"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", InstanceID: "ins-compute", VpcID: "vpc-compute", PrivateIPs: []string{"172.19.32.13"}}, CidrBlock: "172.19.32.0/24"},
	}, []SCFTarget{{Region: "ap-hongkong", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou", RestoreSCFPublic: true, UnbindSCFVPC: true}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SCFUpdated)
	assert.Empty(t, fake.lastVPC)
	assert.Zero(t, fake.detachVPC)
	var skipped bool
	for _, action := range result.Actions {
		if action.Kind == "ccn-detach" && action.Status == "skipped" {
			skipped = true
		}
	}
	assert.True(t, skipped)
}

func TestCollectSCFRestoreTargetsIncludesIdleAndBlacklistedRegions(t *testing.T) {
	manifest := setupconfig.Manifest{
		SCFFetcher: setupconfig.SCFFetcher{
			Enabled: true,
			Spaces: []setupconfig.SCFFetcherSpace{
				{
					SpaceID: "crypto", FunctionPrefix: "moox-fetcher-crypto-binance",
					PublicNetStatus: "ENABLE", RegionBlacklist: []string{"ap-guangzhou"},
					Regions: []setupconfig.SCFFetcherRegion{
						{Region: "ap-hongkong", Enabled: true, FunctionCount: 40},
						{Region: "ap-guangzhou", Enabled: true, FunctionCount: 11},
						{Region: "ap-shanghai", Enabled: false, FunctionCount: 0},
					},
				},
			},
		},
	}
	active := CollectSCFTargets(manifest)
	regions := map[string]struct{}{}
	for _, item := range active {
		regions[item.Region] = struct{}{}
	}
	_, hasGZ := regions["ap-guangzhou"]
	_, hasSH := regions["ap-shanghai"]
	assert.False(t, hasGZ)
	assert.False(t, hasSH)
	assert.Contains(t, regions, "ap-hongkong")

	restore := CollectSCFRestoreTargets(manifest)
	restoreRegions := map[string]struct{}{}
	for _, item := range restore {
		restoreRegions[item.Region] = struct{}{}
	}
	assert.Contains(t, restoreRegions, "ap-guangzhou")
	assert.Contains(t, restoreRegions, "ap-shanghai")
	assert.Contains(t, restoreRegions, "ap-hongkong")
}

type fakeCloud struct {
	ensureCCN  int
	attachVPC  int
	detachVPC  int
	acceptCCN  int
	updateSCF  int
	updateErrs int
	vpcs       map[string]tencent.VpcInfo
	subnets    map[string]tencent.SubnetInfo
	zones      map[string][]string
	functions  []tencent.SCFFunction
	function   tencent.SCFFunction
	lastEnv    map[string]string
	lastVPC    string
	lastSubnet string
	lighthouse []tencent.CCNAttachment
}

func (f *fakeCloud) LookupHost(context.Context, string, []string) (tencent.CloudInstance, error) {
	return tencent.CloudInstance{}, nil
}
func (f *fakeCloud) DescribeVpc(context.Context, string, string) (tencent.VpcInfo, error) {
	return tencent.VpcInfo{}, nil
}
func (f *fakeCloud) EnsureCCN(context.Context, string, string) (tencent.CCNInfo, bool, error) {
	f.ensureCCN++
	return tencent.CCNInfo{CcnID: "ccn-1", Name: DefaultCCNName}, true, nil
}
func (f *fakeCloud) ListCCNAttachments(context.Context, string, string) ([]tencent.CCNAttachment, error) {
	return nil, nil
}
func (f *fakeCloud) AttachVPC(context.Context, string, string, string, string) error {
	f.attachVPC++
	return nil
}
func (f *fakeCloud) FindCCN(context.Context, string, string) (tencent.CCNInfo, bool, error) {
	return tencent.CCNInfo{CcnID: "ccn-1", Name: DefaultCCNName}, true, nil
}
func (f *fakeCloud) DetachVPC(context.Context, string, string, string, string) error {
	f.detachVPC++
	return nil
}
func (f *fakeCloud) AcceptCCN(context.Context, string, string, string, string, string) error {
	f.acceptCCN++
	return nil
}
func (f *fakeCloud) AttachLighthouseCCN(context.Context, string, string) error { return nil }
func (f *fakeCloud) DescribeLighthouseCCN(context.Context, string) ([]tencent.CCNAttachment, error) {
	return f.lighthouse, nil
}
func (f *fakeCloud) EnsureVpc(_ context.Context, region, name, cidr string) (tencent.VpcInfo, bool, error) {
	if info, ok := f.vpcs[region+"/"+name]; ok {
		return info, false, nil
	}
	return tencent.VpcInfo{VpcID: "vpc-" + region, Name: name, CidrBlock: cidr, Region: region}, true, nil
}
func (f *fakeCloud) EnsureSubnet(_ context.Context, region, vpcID, name, cidr, zone string) (tencent.SubnetInfo, bool, error) {
	if info, ok := f.subnets[region+"/"+vpcID+"/"+name]; ok {
		return info, false, nil
	}
	return tencent.SubnetInfo{SubnetID: "subnet-" + region, VpcID: vpcID, Name: name, CidrBlock: cidr, Zone: zone}, true, nil
}
func (f *fakeCloud) DescribeZones(_ context.Context, region string) ([]string, error) {
	if zones := f.zones[region]; len(zones) > 0 {
		return zones, nil
	}
	return []string{region + "-1"}, nil
}
func (f *fakeCloud) EnsureSecurityGroup(context.Context, string, []string, tencent.CreateFirewallRulesOptions) (bool, error) {
	return false, nil
}
func (f *fakeCloud) EnsureLighthouseFirewall(context.Context, string, string, tencent.CreateFirewallRulesOptions) (bool, error) {
	return false, nil
}
func (f *fakeCloud) ListSCF(context.Context, string, string, []string) ([]tencent.SCFFunction, error) {
	return f.functions, nil
}
func (f *fakeCloud) GetSCF(context.Context, string, string, string) (tencent.SCFFunction, error) {
	return f.function, nil
}
func (f *fakeCloud) UpdateSCF(_ context.Context, _, _, _, vpcID, subnetID, _ string, env map[string]string) error {
	f.updateSCF++
	f.lastVPC = vpcID
	f.lastSubnet = subnetID
	f.lastEnv = env
	if f.updateErrs > 0 {
		f.updateErrs--
		return fmt.Errorf("FailedOperation.UpdateFunctionConfiguration: 当前函数处于Updating状态，无法进行此操作，请稍后重试。")
	}
	return nil
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
}

func TestReplacePrivateIPWithPublic(t *testing.T) {
	assert.Equal(t, "ip://146.56.196.204:11003", replaceIP("ip://10.1.2.8:11003", "10.1.2.8", "146.56.196.204"))
	assert.True(t, strings.Contains(PrivateServicePorts(4222)[0], "4222"))
}
