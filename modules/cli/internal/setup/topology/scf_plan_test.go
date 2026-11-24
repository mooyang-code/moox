package topology

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func scfPlanTestConfig() *Config {
	account := SCFCloudAccountConfig{AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "secret", AppID: "1", COSRegion: "ap-guangzhou", COSBucket: "bucket"}
	return &Config{
		HostCatalog: map[string]HostDefinition{
			"control":  {Address: "1.1.1.1", Region: "ap-guangzhou", PrivateAddress: "10.0.0.1"},
			"storage":  {Address: "2.2.2.2", Region: "ap-nanjing", PrivateAddress: "10.206.0.5", ExternalEntry: true},
			"overseas": {Address: "3.3.3.3", Region: "ap-hongkong", PrivateAddress: "172.19.32.13", ExternalEntry: true},
		},
		Deployments: map[string][]string{"control": {"admin", "collector"}, "storage": {"storage-primary"}, "overseas": {"trade"}},
		SCFPlan: SCFPlan{
			Enabled: true, CloudAccount: account,
			Regions: map[string]SCFPlanRegion{"ap-hongkong": {VPCID: "vpc-hk", SubnetID: "subnet-hk"}},
			Spaces:  []SCFPlanSpace{{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong"}}}},
		},
	}
}

func TestSCFPlanDefaultsAndRenderedCollectorConfig(t *testing.T) {
	manifest := scfPlanTestConfig()
	require.NoError(t, manifest.CheckSCFPlan())
	require.Equal(t, 128, manifest.SCFPlan.MemoryMB)
	require.Equal(t, 50, manifest.SCFPlan.SymbolsPerFunction)
	require.Equal(t, "moox-collector-crypto-plan", manifest.SCFPlan.Spaces[0].PackageName)
	require.Equal(t, "moox-crypto-plan", manifest.SCFPlan.Spaces[0].Namespace)

	rendered, err := RenderCollectorRuntimeConfig(manifest, []byte("database: {path: keep.db}\nscf_plan: {package_id: stale}\n"))
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
	require.Equal(t, map[string]any{"ap-hongkong": map[string]any{"entry_address": "172.19.32.13:11004", "entry_host": "overseas"}}, plan["entry_routes"])
	require.Equal(t, map[string]any{"crypto": map[string]any{"package_name": "moox-collector-crypto-plan", "namespace": "moox-crypto-plan"}}, plan["spaces"])

	// 渲染幂等：对自己的输出再渲染结果不变。
	again, err := RenderCollectorRuntimeConfig(manifest, rendered)
	require.NoError(t, err)
	require.Equal(t, string(rendered), string(again))
}

func TestSCFPlanDisabledRemovesTheRenderedBlocks(t *testing.T) {
	manifest := scfPlanTestConfig()
	require.NoError(t, manifest.CheckSCFPlan())
	rendered, err := RenderCollectorRuntimeConfig(manifest, nil)
	require.NoError(t, err)
	manifest.SCFPlan.Enabled = false
	rendered, err = RenderCollectorRuntimeConfig(manifest, rendered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	for _, key := range []string{"scf_regions", "scf_markets", "scf_plan"} {
		require.NotContains(t, got, key)
	}
}

func TestSCFPlanValidationRejectsIncompleteOrInconsistentSettings(t *testing.T) {
	cases := map[string]func(*Config){
		"no cloud account":            func(m *Config) { m.SCFPlan.CloudAccount = SCFCloudAccountConfig{} },
		"no spaces":                   func(m *Config) { m.SCFPlan.Spaces = nil },
		"no markets":                  func(m *Config) { m.SCFPlan.Spaces[0].Markets = nil },
		"missing vpc":                 func(m *Config) { m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{} },
		"unused region without entry": func(m *Config) { m.SCFPlan.Regions["ap-shanghai"] = SCFPlanRegion{VPCID: "v", SubnetID: "s"} },
		"bad network": func(m *Config) {
			m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "cable", VPCID: "v", SubnetID: "s"}
		},
		"public with vpc": func(m *Config) {
			m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "public", VPCID: "v", SubnetID: "s", EntryHost: "storage"}
		},
		"public without entry host": func(m *Config) { m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "public"} },
		"public entry is not an entry": func(m *Config) {
			m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "public", EntryHost: "control"}
		},
		"public entry is unknown": func(m *Config) {
			m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "public", EntryHost: "nobody"}
		},
		"public entry has no address": func(m *Config) {
			m.SCFPlan.Regions["ap-hongkong"] = SCFPlanRegion{Network: "public", EntryHost: "storage"}
			definition := m.HostCatalog["storage"]
			definition.Address = ""
			m.HostCatalog["storage"] = definition
		},
		"unsupported region":             func(m *Config) { m.SCFPlan.Spaces[0].Markets["spot"] = []string{"mars-1"} },
		"unsupported backfill region":    func(m *Config) { m.SCFPlan.Spaces[0].BackfillRegion = "mars-1" },
		"backfill region has no network": func(m *Config) { m.SCFPlan.Spaces[0].BackfillRegion = "ap-nanjing" },
		"no entry host": func(m *Config) {
			definition := m.HostCatalog["overseas"]
			definition.ExternalEntry = false
			m.HostCatalog["overseas"] = definition
		},
		"entry has no VPC ip": func(m *Config) {
			definition := m.HostCatalog["overseas"]
			definition.PrivateAddress = ""
			m.HostCatalog["overseas"] = definition
		},
		"duplicate space": func(m *Config) { m.SCFPlan.Spaces = append(m.SCFPlan.Spaces, m.SCFPlan.Spaces[0]) },
		"bad space id":    func(m *Config) { m.SCFPlan.Spaces[0].SpaceID = "Crypto Space" },
		"bad memory":      func(m *Config) { m.SCFPlan.MemoryMB = 16 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := scfPlanTestConfig()
			mutate(manifest)
			require.Error(t, manifest.CheckSCFPlan())
		})
	}
	// 关闭时不校验。
	manifest := scfPlanTestConfig()
	manifest.SCFPlan.Enabled, manifest.SCFPlan.Spaces = false, nil
	require.NoError(t, manifest.CheckSCFPlan())
}

// public 地域不绑 VPC，入口用 entry_host 的公网地址；没被市场使用的地域（撤地域的过渡期）照样渲染网络与入口。
func TestSCFPlanPublicRegionAndUnusedRegionAreRendered(t *testing.T) {
	manifest := scfPlanTestConfig()
	manifest.SCFPlan.Regions["ap-shanghai"] = SCFPlanRegion{Network: " Public ", EntryHost: "storage"}
	manifest.SCFPlan.Regions["ap-nanjing"] = SCFPlanRegion{VPCID: "vpc-nj", SubnetID: "subnet-nj"}
	manifest.SCFPlan.Spaces[0].Markets["spot"] = []string{"ap-hongkong", "ap-shanghai"}
	require.NoError(t, manifest.CheckSCFPlan())
	require.Equal(t, SCFNetworkPublic, manifest.SCFPlan.Regions["ap-shanghai"].Network)
	require.Equal(t, SCFNetworkVPC, manifest.SCFPlan.Regions["ap-hongkong"].Network)
	address, host, err := manifest.SCFPlanEntry("ap-shanghai")
	require.NoError(t, err)
	require.Equal(t, "2.2.2.2:11004", address)
	require.Equal(t, "storage", host)

	rendered, err := RenderCollectorRuntimeConfig(manifest, nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Equal(t, map[string]any{
		"ap-hongkong": map[string]any{"vpc_id": "vpc-hk", "subnet_id": "subnet-hk"},
		"ap-nanjing":  map[string]any{"vpc_id": "vpc-nj", "subnet_id": "subnet-nj"},
		"ap-shanghai": map[string]any{"network": "public"},
	}, got["scf_regions"])
	require.Equal(t, map[string]any{
		"ap-hongkong": map[string]any{"entry_address": "172.19.32.13:11004", "entry_host": "overseas"},
		"ap-nanjing":  map[string]any{"entry_address": "10.206.0.5:11004", "entry_host": "storage"},
		"ap-shanghai": map[string]any{"entry_address": "2.2.2.2:11004", "entry_host": "storage"},
	}, got["scf_plan"].(map[string]any)["entry_routes"])
}

func TestSCFPlanEntrySelectionHandlesSeveralHostsInOneRegion(t *testing.T) {
	manifest := scfPlanTestConfig()
	manifest.HostCatalog["compute-2"] = HostDefinition{Address: "4.4.4.4", Region: "ap-hongkong", PrivateAddress: "172.19.32.14", ExternalEntry: true}
	manifest.HostCatalog["compute-0"] = HostDefinition{Address: "5.5.5.5", Region: "ap-hongkong", ExternalEntry: true} // 没有私网地址

	// 两台都有私网地址：必须显式指定。
	_, _, err := manifest.SCFPlanEntry("ap-hongkong")
	require.ErrorContains(t, err, "entry_host")
	require.ErrorContains(t, manifest.CheckSCFPlan(), "entry_host")

	region := manifest.SCFPlan.Regions["ap-hongkong"]
	region.EntryHost = "compute-2"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	address, id, err := manifest.SCFPlanEntry("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "172.19.32.14:11004", address)
	require.Equal(t, "compute-2", id)
	require.NoError(t, manifest.CheckSCFPlan())

	// 指定的主机没有私网地址、或不在该地域：明确拒绝。
	region.EntryHost = "compute-0"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	_, _, err = manifest.SCFPlanEntry("ap-hongkong")
	require.ErrorContains(t, err, "private_address")
	region.EntryHost = "storage"
	manifest.SCFPlan.Regions["ap-hongkong"] = region
	_, _, err = manifest.SCFPlanEntry("ap-hongkong")
	require.ErrorContains(t, err, "not an external entry host in that region")

	// 第一台（按名称排序）没有私网地址、只有一台可用：自动选可用的那台。
	manifest = scfPlanTestConfig()
	manifest.HostCatalog["a-compute"] = HostDefinition{Address: "6.6.6.6", Region: "ap-hongkong", ExternalEntry: true}
	address, id, err = manifest.SCFPlanEntry("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "172.19.32.13:11004", address)
	require.Equal(t, "overseas", id)
}

// 渲染产物夹具：Collector 的 bootstrap 测试用真实 Load 读取同一份文件，两端字段名、键与必填项不一致时任一侧测试都会失败。
// 渲染逻辑变化后用 MOOX_UPDATE_GOLDEN=1 重新生成。
const scfPlanRenderedFixture = "testdata/scf_plan_rendered.yaml"

func TestSCFPlanRenderedCollectorConfigMatchesTheSharedFixture(t *testing.T) {
	manifest := scfPlanTestConfig()
	manifest.SCFPlan.Spaces = append(manifest.SCFPlan.Spaces, SCFPlanSpace{SpaceID: "stockcn", Markets: map[string][]string{"equity": {"ap-hongkong", "ap-shanghai"}}})
	// 公网地域：Collector 也要能加载 network: public 与公网入口。
	manifest.SCFPlan.Regions["ap-shanghai"] = SCFPlanRegion{Network: SCFNetworkPublic, EntryHost: "storage"}
	require.NoError(t, manifest.CheckSCFPlan())
	// 与默认部署一致的出口与保留策略，使产物是 Collector 能真实加载的完整配置。
	manifest.EgressProxy = EgressProxy{HTTPDomains: []string{"*.binance.com"}}
	manifest.CollectorRetention = CollectorRetention{MaintenanceInterval: "1m", MaintenanceOffset: "35s", MaintenanceTimeout: "20s", MaxRowsPerPass: 50000}
	// 模板与新版 Collector 的 config/app.yaml 一样带 delist 段（渲染只替换、不新增）。
	rendered, err := RenderCollectorRuntimeConfig(manifest, []byte("gateway_client:\n  caller: collector\n  key_file: ../../secrets/host.key\ndelist:\n  auto_detect: true\n"))
	require.NoError(t, err)
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(scfPlanRenderedFixture, rendered, 0o644))
	}
	expected, err := os.ReadFile(scfPlanRenderedFixture)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(rendered))
}

// backfill_region：补采函数单独放的地域，要有自己的 [scf_plan.regions]，渲染进 Collector 的 scf_regions、entry_routes 与空间配置。
func TestSCFPlanBackfillRegionIsRendered(t *testing.T) {
	manifest := scfPlanTestConfig()
	manifest.SCFPlan.Regions["ap-nanjing"] = SCFPlanRegion{VPCID: "vpc-nj", SubnetID: "subnet-nj"}
	manifest.SCFPlan.Spaces[0].BackfillRegion = " AP-Nanjing "
	require.NoError(t, manifest.CheckSCFPlan())
	require.Equal(t, []string{"ap-hongkong", "ap-nanjing"}, manifest.SCFPlanConfiguredRegions())

	rendered, err := RenderCollectorRuntimeConfig(manifest, []byte("database: {path: keep.db}\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	require.Contains(t, got["scf_regions"], "ap-nanjing")
	plan := got["scf_plan"].(map[string]any)
	require.Equal(t, map[string]any{"entry_address": "10.206.0.5:11004", "entry_host": "storage"}, plan["entry_routes"].(map[string]any)["ap-nanjing"])
	require.Equal(t, "ap-nanjing", plan["spaces"].(map[string]any)["crypto"].(map[string]any)["backfill_region"])
	require.Equal(t, map[string]any{"crypto": map[string]any{"spot": []any{"ap-hongkong"}, "swap": []any{"ap-hongkong"}}}, got["scf_markets"], "定时函数的地域不变")
}
