package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func scfPlanTestManifest() Manifest {
	account := SCFCloudAccountConfig{AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "secret", AppID: "1", COSRegion: "ap-guangzhou", COSBucket: "bucket"}
	return Manifest{
		HostCatalog: map[string]HostDefinition{
			"control":   {Address: "1.1.1.1", Region: "ap-guangzhou", PrivateAddress: "10.0.0.1"},
			"storage":   {Address: "2.2.2.2", Region: "ap-nanjing", PrivateAddress: "10.206.0.5"},
			"compute-1": {Address: "3.3.3.3", Region: "ap-hongkong", PrivateAddress: "172.19.32.13"},
		},
		Placements: map[string][]string{"control": {"admin", "collector"}, "storage": {"storage-primary", "access"}, "compute-1": {"access"}},
		SCFPlan: SCFPlan{
			Enabled: true, CloudAccount: account,
			Regions: map[string]SCFPlanRegion{"ap-hongkong": {VPCID: "vpc-hk", SubnetID: "subnet-hk"}},
			Spaces:  []SCFPlanSpace{{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong"}}}},
		},
	}
}

func TestSCFPlanDefaultsAndRenderedCollectorConfig(t *testing.T) {
	manifest := scfPlanTestManifest()
	require.NoError(t, validateSCFPlan(&manifest))
	require.Equal(t, 128, manifest.SCFPlan.MemoryMB)
	require.Equal(t, 50, manifest.SCFPlan.SymbolsPerFunction)
	require.Equal(t, "moox-collector-crypto-plan", manifest.SCFPlan.Spaces[0].PackageName)
	require.Equal(t, "moox-crypto-plan", manifest.SCFPlan.Spaces[0].Namespace)

	rendered, err := RenderCollectorRuntimeConfig(&Snapshot{Manifest: manifest}, []byte("database: {path: keep.db}\nscf_plan: {package_id: stale}\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Equal(t, "keep.db", got["database"].(map[string]any)["path"])
	require.Equal(t, map[string]any{"ap-hongkong": map[string]any{"vpc_id": "vpc-hk", "subnet_id": "subnet-hk"}}, got["scf_regions"])
	require.Equal(t, map[string]any{"crypto": map[string]any{"spot": []any{"ap-hongkong"}, "swap": []any{"ap-hongkong"}}}, got["scf_markets"])
	plan := got["scf_plan"].(map[string]any)
	require.Equal(t, "tencent-scf", plan["cloud_account_id"])
	require.Equal(t, 128, plan["memory_mb"])
	require.Equal(t, 50, plan["symbols_per_function"])
	require.NotContains(t, plan, "package_id", "代码包只写名称，固定 ID 不再渲染")
	require.Equal(t, map[string]any{"ap-hongkong": map[string]any{"access_address": "172.19.32.13:11004", "access_id": "access@compute-1"}}, plan["access_routes"])
	require.Equal(t, map[string]any{"crypto": map[string]any{"package_name": "moox-collector-crypto-plan", "namespace": "moox-crypto-plan"}}, plan["spaces"])

	// 渲染幂等：对自己的输出再渲染结果不变。
	again, err := RenderCollectorRuntimeConfig(&Snapshot{Manifest: manifest}, rendered)
	require.NoError(t, err)
	require.Equal(t, string(rendered), string(again))
}

func TestSCFPlanDisabledRemovesTheRenderedBlocks(t *testing.T) {
	manifest := scfPlanTestManifest()
	require.NoError(t, validateSCFPlan(&manifest))
	rendered, err := RenderCollectorRuntimeConfig(&Snapshot{Manifest: manifest}, nil)
	require.NoError(t, err)
	manifest.SCFPlan.Enabled = false
	rendered, err = RenderCollectorRuntimeConfig(&Snapshot{Manifest: manifest}, rendered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	for _, key := range []string{"scf_regions", "scf_markets", "scf_plan"} {
		require.NotContains(t, got, key)
	}
}

func TestSCFPlanValidationRejectsIncompleteOrInconsistentSettings(t *testing.T) {
	cases := map[string]func(*Manifest){
		"no cloud account":   func(m *Manifest) { m.SCFPlan.CloudAccount = SCFCloudAccountConfig{} },
		"no spaces":          func(m *Manifest) { m.SCFPlan.Spaces = nil },
		"no markets":         func(m *Manifest) { m.SCFPlan.Spaces[0].Markets = nil },
		"missing vpc":        func(m *Manifest) { m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{} },
		"unused region":      func(m *Manifest) { m.SCFPlan.Regions["ap-nanjing"] = SCFPlanRegion{VPCID: "v", SubnetID: "s"} },
		"unsupported region": func(m *Manifest) { m.SCFPlan.Spaces[0].Markets["spot"] = []string{"mars-1"} },
		"no access host":     func(m *Manifest) { m.Placements["compute-1"] = nil },
		"access has no VPC ip": func(m *Manifest) {
			definition := m.HostCatalog["compute-1"]
			definition.PrivateAddress = ""
			m.HostCatalog["compute-1"] = definition
		},
		"duplicate space": func(m *Manifest) { m.SCFPlan.Spaces = append(m.SCFPlan.Spaces, m.SCFPlan.Spaces[0]) },
		"bad space id":    func(m *Manifest) { m.SCFPlan.Spaces[0].SpaceID = "Crypto Space" },
		"bad memory":      func(m *Manifest) { m.SCFPlan.MemoryMB = 16 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := scfPlanTestManifest()
			mutate(&manifest)
			require.Error(t, validateSCFPlan(&manifest))
		})
	}
	// 关闭时不校验。
	manifest := scfPlanTestManifest()
	manifest.SCFPlan.Enabled, manifest.SCFPlan.Spaces = false, nil
	require.NoError(t, validateSCFPlan(&manifest))
}

func TestSCFPlanAccessSelectionHandlesSeveralHostsInOneRegion(t *testing.T) {
	manifest := scfPlanTestManifest()
	manifest.HostCatalog["compute-2"] = HostDefinition{Address: "4.4.4.4", Region: "ap-hongkong", PrivateAddress: "172.19.32.14"}
	manifest.HostCatalog["compute-0"] = HostDefinition{Address: "5.5.5.5", Region: "ap-hongkong"} // 没有私网地址
	manifest.Placements["compute-2"] = []string{"access"}
	manifest.Placements["compute-0"] = []string{"access"}

	// 两台都有私网地址：必须显式指定。
	_, _, err := manifest.SCFPlanAccess("ap-hongkong")
	require.ErrorContains(t, err, "access_host")
	require.ErrorContains(t, validateSCFPlan(&manifest), "access_host")

	region := manifest.SCFPlan.Regions["ap-hongkong"]
	region.AccessHost = "compute-2"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	address, id, err := manifest.SCFPlanAccess("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "172.19.32.14:11004", address)
	require.Equal(t, "access@compute-2", id)
	require.NoError(t, validateSCFPlan(&manifest))

	// 指定的主机没有私网地址、或不在该地域：明确拒绝。
	region.AccessHost = "compute-0"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	_, _, err = manifest.SCFPlanAccess("ap-hongkong")
	require.ErrorContains(t, err, "private_address")
	region.AccessHost = "storage"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	_, _, err = manifest.SCFPlanAccess("ap-hongkong")
	require.ErrorContains(t, err, "not an access host in that region")

	// 第一台（按名称排序）没有私网地址、只有一台可用：自动选可用的那台。
	manifest = scfPlanTestManifest()
	manifest.HostCatalog["a-compute"] = HostDefinition{Address: "6.6.6.6", Region: "ap-hongkong"}
	manifest.Placements["a-compute"] = []string{"access"}
	address, id, err = manifest.SCFPlanAccess("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "172.19.32.13:11004", address)
	require.Equal(t, "access@compute-1", id)
}

// 渲染产物夹具：Collector 的 bootstrap 测试用真实 Load 读取同一份文件，两端字段名、键与必填项不一致时任一侧测试都会失败。
// 渲染逻辑变化后用 MOOX_UPDATE_GOLDEN=1 重新生成。
const scfPlanRenderedFixture = "testdata/scf_plan_rendered.yaml"

func TestSCFPlanRenderedCollectorConfigMatchesTheSharedFixture(t *testing.T) {
	manifest := scfPlanTestManifest()
	manifest.SCFPlan.Spaces = append(manifest.SCFPlan.Spaces, SCFPlanSpace{SpaceID: "stockcn", Markets: map[string][]string{"equity": {"ap-hongkong"}}})
	require.NoError(t, validateSCFPlan(&manifest))
	// 与默认部署一致的出口与保留策略，使产物是 Collector 能真实加载的完整配置。
	manifest.EgressProxy = EgressProxy{HTTPDomains: []string{"*.binance.com"}, DNS: EgressDNS{Domains: []string{"fapi.binance.com"}, RefreshIntervalSeconds: 300, RequestTimeoutMS: 3000, LookupTimeoutMS: 1500, ProbeTimeoutMS: 500, ProbePort: 443, CacheTTLSeconds: 300, MaxIPsPerDomain: 4}}
	manifest.CollectorRetention = CollectorRetention{MaintenanceInterval: "1m", MaintenanceOffset: "35s", MaintenanceTimeout: "20s", MaxRowsPerPass: 50000}
	rendered, err := RenderCollectorRuntimeConfig(&Snapshot{Manifest: manifest}, []byte("gateway_client:\n  caller: collector\n  key_file: ./caller-collector.key\n"))
	require.NoError(t, err)
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(scfPlanRenderedFixture, rendered, 0o644))
	}
	expected, err := os.ReadFile(scfPlanRenderedFixture)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(rendered))
}
