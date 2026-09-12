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

func TestBuildPlanUsesCCNForMixedLighthouseAndCVM(t *testing.T) {
	plan := BuildPlan(Options{CCNName: DefaultCCNName}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", InstanceID: "lhins-1", PrivateIPs: []string{"10.0.1.12"}}, CidrBlock: "10.0.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", InstanceID: "ins-1", VpcID: "vpc-storage", SubnetID: "subnet-storage", PrivateIPs: []string{"10.1.2.8"}}, CidrBlock: "10.1.0.0/16"},
	}, []SCFTarget{
		{Region: "ap-hongkong", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"},
		{Region: "ap-singapore", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"},
	}, []string{"11003"})
	require.True(t, plan.NeedCCN)
	require.True(t, plan.NeedOverseasCCN)
	require.False(t, plan.NeedMainlandCCN)
	require.Equal(t, DefaultCCNName+"-global", plan.OverseasCCN)
	require.Equal(t, "10.1.2.8", plan.Recommended.StoragePrivateIP)
	require.Equal(t, "ip://10.1.2.8:11003", plan.Recommended.SCFGatewayTarget)
	var hk, sg PlannedSCFBind
	for _, bind := range plan.SCF {
		if bind.Region == "ap-hongkong" {
			hk = bind
		}
		if bind.Region == "ap-singapore" {
			sg = bind
		}
	}
	assert.Equal(t, "vpc-storage", hk.VpcID)
	assert.Equal(t, "subnet-storage", hk.SubnetID)
	assert.Equal(t, "moox-scf-ap-singapore", sg.VpcName)
	assert.NotEmpty(t, sg.SCFTarget.Region)
	require.Len(t, plan.VPCs, 1)
	assert.Equal(t, "ap-singapore", plan.VPCs[0].Region)
	assert.Equal(t, "overseas", plan.VPCs[0].Area)
}

func TestBuildPlanSplitsMainlandAndOverseasCCN(t *testing.T) {
	plan := BuildPlan(Options{}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", PrivateIPs: []string{"10.1.12.13"}}, CidrBlock: "10.1.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", VpcID: "vpc-storage", SubnetID: "subnet-storage", PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{
		{Region: "ap-guangzhou", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}},
		{Region: "ap-singapore", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}},
	}, []string{"11003"})
	require.True(t, plan.NeedMainlandCCN)
	require.False(t, plan.NeedOverseasCCN, "a single overseas SCF vpc does not need its own ccn")
	assert.Equal(t, "mainland", plan.Recommended.StorageArea)
	assert.Contains(t, plan.CIDRsByArea["mainland"], "10.206.0.0/16")
	var gz, sg PlannedSCFBind
	for _, bind := range plan.SCF {
		if bind.Region == "ap-guangzhou" {
			gz = bind
		}
		if bind.Region == "ap-singapore" {
			sg = bind
		}
	}
	assert.Equal(t, "mainland", gz.Area)
	assert.Equal(t, "overseas", sg.Area)
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

func TestApplyAcceptsLighthousePendingCCN(t *testing.T) {
	fake := &fakeCloud{
		lighthouse: []tencent.CCNAttachment{{
			CcnID: "ccn-1", InstanceID: "vpc-lh", InstanceRegion: "ap-guangzhou",
			InstanceType: "VPC", CidrBlock: "10.1.0.0/16", CidrBlocks: []string{"10.1.0.0/16"},
			State: "PENDING",
		}},
	}
	plan := BuildPlan(Options{}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}, Instance: tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-guangzhou", PrivateIPs: []string{"10.1.12.13"}}, CidrBlock: "10.1.0.0/16"},
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", VpcID: "vpc-storage", SecurityGroupIDs: []string{"sg-1"}, PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, nil, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou"}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, fake.acceptCCN)
	assert.Contains(t, result.Plan.CIDRsByArea["mainland"], "10.1.0.0/16")
}

func TestApplyRetriesSCFUpdating(t *testing.T) {
	fake := &fakeCloud{
		updateErrs: 1,
		vpcs:       map[string]tencent.VpcInfo{"ap-shanghai/moox-scf-ap-shanghai": {VpcID: "vpc-scf", CidrBlock: "10.81.0.0/16", Region: "ap-shanghai"}},
		subnets:    map[string]tencent.SubnetInfo{"ap-shanghai/vpc-scf/moox-scf-ap-shanghai-a": {SubnetID: "subnet-scf", VpcID: "vpc-scf"}},
		zones:      map[string][]string{"ap-shanghai": {"ap-shanghai-1"}},
		functions:  []tencent.SCFFunction{{Region: "ap-shanghai", Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0"}},
		function:   tencent.SCFFunction{Namespace: "default", FunctionName: "moox-fetcher-stockcn-ap-shanghai-0"},
	}
	plan := BuildPlan(Options{}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-nanjing", InstanceID: "ins-1", VpcID: "vpc-storage", SecurityGroupIDs: []string{"sg-1"}, PrivateIPs: []string{"10.206.0.5"}}, CidrBlock: "10.206.0.0/16"},
	}, []SCFTarget{{Region: "ap-shanghai", Namespace: "default", Prefixes: []string{"moox-fetcher-stockcn"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou"}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SCFUpdated)
	assert.Equal(t, 2, fake.updateSCF)
}

func TestIsRetryableSCFUpdate(t *testing.T) {
	assert.True(t, isRetryableSCFUpdate(fmt.Errorf("FailedOperation.UpdateFunctionConfiguration: 当前函数处于Updating状态，无法进行此操作，请稍后重试。")))
	assert.False(t, isRetryableSCFUpdate(fmt.Errorf("InvalidParameterValue: FunctionName")))
}

func TestApplyBindsSCFAndCreatesCCN(t *testing.T) {
	fake := &fakeCloud{
		vpcs:      map[string]tencent.VpcInfo{"ap-singapore/moox-scf-ap-singapore": {VpcID: "vpc-scf", CidrBlock: "10.85.0.0/16", Region: "ap-singapore"}},
		subnets:   map[string]tencent.SubnetInfo{"ap-singapore/vpc-scf/moox-scf-ap-singapore-a": {SubnetID: "subnet-scf", VpcID: "vpc-scf"}},
		zones:     map[string][]string{"ap-singapore": {"ap-singapore-1"}},
		functions: []tencent.SCFFunction{{Region: "ap-singapore", Namespace: "default", FunctionName: "moox-fetcher-crypto-binance-ap-singapore-0"}},
		function:  tencent.SCFFunction{Namespace: "default", FunctionName: "moox-fetcher-crypto-binance-ap-singapore-0", Environment: map[string]string{"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://146.56.196.204:11003"}},
	}
	plan := BuildPlan(Options{UpdateSCFGateway: true}, []ResolvedHost{
		{HostTarget: HostTarget{Name: "storage", Address: "146.56.196.204", Roles: []string{"storage"}}, Instance: tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-hongkong", InstanceID: "ins-1", VpcID: "vpc-storage", SecurityGroupIDs: []string{"sg-1"}, PrivateIPs: []string{"10.1.2.8"}}, CidrBlock: "10.1.0.0/16"},
	}, []SCFTarget{{Region: "ap-singapore", Namespace: "default", Prefixes: []string{"moox-fetcher-crypto-binance"}, PublicNetStatus: "ENABLE"}}, []string{"11003"})
	result, err := Apply(context.Background(), fake, Options{HomeRegion: "ap-guangzhou", UpdateSCFGateway: true}, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, fake.ensureCCN)
	assert.Equal(t, 2, fake.attachVPC)
	assert.Equal(t, 1, result.SCFUpdated)
	require.NotEmpty(t, fake.lastEnv)
	assert.Equal(t, "ip://10.1.2.8:11003", fake.lastEnv["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
}

type fakeCloud struct {
	ensureCCN  int
	attachVPC  int
	acceptCCN  int
	updateSCF  int
	updateErrs int
	vpcs       map[string]tencent.VpcInfo
	subnets    map[string]tencent.SubnetInfo
	zones      map[string][]string
	functions  []tencent.SCFFunction
	function   tencent.SCFFunction
	lastEnv    map[string]string
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
func (f *fakeCloud) UpdateSCF(_ context.Context, _, _, _, _, _, _ string, env map[string]string) error {
	f.updateSCF++
	f.lastEnv = env
	if f.updateErrs > 0 {
		f.updateErrs--
		return fmt.Errorf("FailedOperation.UpdateFunctionConfiguration: 当前函数处于Updating状态，无法进行此操作，请稍后重试。")
	}
	return nil
}

func TestPlannedProbesMainlandAndOverseas(t *testing.T) {
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
	assert.True(t, controlToStorage.SameArea)
	assert.Equal(t, "closed", computeToStorage.Expect)
	assert.False(t, computeToStorage.SameArea)
	assert.NoError(t, MainlandProbesFailed([]Probe{{SameArea: true, Expect: "open", Status: "open"}}))
	assert.Error(t, MainlandProbesFailed([]Probe{{From: "control", To: "storage", Port: "11003", SameArea: true, Expect: "open", Status: "closed"}}))
}

func TestCCNAcceptIdentityForcesVPC(t *testing.T) {
	typ, id, skip := ccnAcceptIdentity(tencent.CCNAttachment{InstanceID: "lhins-a7yikq89", InstanceType: "LIGHTHOUSE"})
	assert.True(t, skip)
	assert.Empty(t, typ)
	assert.Empty(t, id)
	typ, id, skip = ccnAcceptIdentity(tencent.CCNAttachment{InstanceID: "vpc-lgg9zeq5", InstanceType: "LIGHTHOUSE"})
	assert.False(t, skip)
	assert.Equal(t, "VPC", typ)
	assert.Equal(t, "vpc-lgg9zeq5", id)
}

func TestReplacePublicIP(t *testing.T) {
	assert.Equal(t, "ip://10.1.2.8:11003", replacePublicIP("ip://146.56.196.204:11003", "146.56.196.204", "10.1.2.8"))
	assert.True(t, strings.Contains(PrivateServicePorts(4222)[0], "4222"))
}
