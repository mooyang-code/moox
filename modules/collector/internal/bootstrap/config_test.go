package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/security"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDefaultHealthConfigAndEnvOverride(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")

	cfg := Default()
	if cfg.Health.Addr != ":11412" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, ":11412")
	}

	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16012" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, "127.0.0.1:16012")
	}
}

func TestLoadReadsYAMLAndAppliesEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_DB_PATH", "./override/collector.db")
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")
	t.Setenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET", "ip://127.0.0.1:30100")
	t.Setenv("MOOX_GATEWAY_CALLER", "collector")

	path := writeCollectorConfig(t, `
database:
  path: ./original/collector.db
storage:
  result_data_node_id: result-test
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/collector.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16012", cfg.Health.Addr)
	assert.Equal(t, "result-test", cfg.Storage.ResultDataNodeID)
	assert.Equal(t, "collector", cfg.GatewayClient.Caller)
}

func TestLoadRejectsRemovedCollectorRuntimeGatewayConfig(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `collector_runtime:
  gateway_target: collector.example:11003
  node_id: control
`))
	require.Error(t, err)
}

func TestCollectorRPCListenersUseNativeLoopbackOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	var config struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &config))
	foundCollectMgr := false
	for _, service := range config.Server.Service {
		require.NotEqual(t, 11418, service.Port)
		require.NotContains(t, service.Name, "MarketFetchRuntime")
		if service.Name == "trpc.moox.collector.CollectMgr" {
			foundCollectMgr = true
			require.Equal(t, "127.0.0.1", service.IP)
			require.Equal(t, 11402, service.Port)
			require.Equal(t, "trpc", service.Protocol)
		}
	}
	require.True(t, foundCollectMgr)
}

func TestMarketFetchSpaceIDUsesConfiguredMarketForScheduler(t *testing.T) {
	t.Setenv("MOOX_SPACE_IDS", "")
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	if got := marketFetchSpaceID(); got != "stockcn" {
		t.Fatalf("marketFetchSpaceID() = %q, want stockcn", got)
	}
}

func TestMarketFetchSpaceIDsSupportsMultipleSpacesAndDeduplicates(t *testing.T) {
	got := parseMarketFetchSpaceIDs(" stockcn, crypto, STOCKCN,,crypto ")
	assert.Equal(t, []string{"stockcn", "crypto"}, got)
}

func TestMarketFetchSpaceIDsPrefersPluralEnvironment(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_SPACE_IDS", "crypto,stockcn")
	assert.Equal(t, []string{"crypto", "stockcn"}, marketFetchSpaceIDs())
}

func TestLoadKlineResampleConfig(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, `kline_resample:
  enabled: true
  repair_lookback_buckets: 5
  worker_subject_batch_size: 20
`))
	require.NoError(t, err)
	assert.True(t, cfg.KlineResample.Enabled)
	assert.Equal(t, 5, cfg.KlineResample.RepairLookbackBuckets)
	assert.Equal(t, 100, cfg.KlineResample.MaxClaimsPerTick)
	assert.Equal(t, 20, cfg.KlineResample.WorkerSubjectBatchSize)
}

func TestLoadCollectorRetentionDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "collector_retention:\n  max_rows_per_pass: 25000\n"))
	require.NoError(t, err)
	assert.Equal(t, "1m", cfg.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "35s", cfg.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "20s", cfg.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 25000, cfg.CollectorRetention.MaxRowsPerPass)
}

func TestLoadRejectsInvalidCollectorRetention(t *testing.T) {
	for _, body := range []string{
		"collector_retention:\n  max_rows_per_pass: 0\n",
		"collector_retention:\n  max_rows_per_pass: 50001\n",
		"collector_retention:\n  maintenance_interval: 1m\n  maintenance_timeout: 2m\n",
		// A pass must end before the next minute boundary owned by the market tick.
		"collector_retention:\n  maintenance_offset: 35s\n  maintenance_timeout: 30s\n",
		"collector_retention:\n  maintenance_offset: 1m\n",
		"collector_retention:\n  maintenance_offset: -1s\n",
	} {
		_, err := Load(writeCollectorConfig(t, body))
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), "collector_retention")
	}
}

func TestLoadAcceptsMinimumCollectorRetentionBudget(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "collector_retention:\n  max_rows_per_pass: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.CollectorRetention.MaxRowsPerPass)
}

func TestLoadRejectsInvalidPeriodReadinessRetention(t *testing.T) {
	for _, body := range []string{
		"period_readiness:\n  item_retention: 0\n",
		"period_readiness:\n  parent_retention: -1h\n",
		"period_readiness:\n  parent_retention: 8761h\n",
	} {
		_, err := Load(writeCollectorConfig(t, body))
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), "period_readiness")
	}
}

func TestLoadRejectsInvalidKlineResampleRepairLookback(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `kline_resample:
  repair_lookback_buckets: 11
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repair_lookback_buckets")
}

func TestLoadRejectsExcessiveKlineResampleConcurrency(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `kline_resample:
  worker_concurrency: 251
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker quantities")
}

func TestLoadRejectsExcessiveKlineResampleClaims(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `kline_resample:
  max_claims_per_tick: 1001
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker quantities")
}

func TestLoadRejectsLegacyStorageTargets(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `
storage:
  metadata_target: 127.0.0.1:20100
  access_target: 127.0.0.1:20102
`))
	require.Error(t, err)
}

func TestLoadRejectsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoadEgressProxyUsesSharedGateway(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, `egress_proxy:
  domains: ["*.binance.com", "data-api.binance.vision"]
  dns:
    domains: [FAPI.BINANCE.COM., api.binance.com]
    request_timeout: 2s
`))
	require.NoError(t, err)
	require.True(t, cfg.EgressProxy.DNS.Enabled())
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, cfg.EgressProxy.DNS.Domains)
	require.Equal(t, 2*time.Second, cfg.EgressProxy.DNS.RequestTimeout)
}
func TestLoadRejectsInvalidEgressProxy(t *testing.T) {
	for _, raw := range []string{
		"dns_resolver: {enabled: true}",
		"egress_proxy: {target: 'ip://old.example:11003'}",
		"egress_proxy: {domains: [binance.com.evil/path]}",
		"egress_proxy: {dns: {domains: ['*.binance.com']}}",
		"egress_proxy: {dns: {domains: [api.binance.com, API.BINANCE.COM.]}}",
		"egress_proxy: {dns: {request_timeout: 0s}}",
		"egress_proxy: {dns: {request_timeout: 61s}}",
		"egress_proxy: {dns: {node_id: old}}",
	} {
		_, err := Load(writeCollectorConfig(t, raw))
		require.Error(t, err, raw)
	}
}

func writeCollectorConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestCollectorGatewayIdentityUsesStrictConfiguration(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_CALLER", "wrong-legacy-caller")
	t.Setenv("MOOX_COLLECTOR_STORAGE_RPC_KEY_ID", "obsolete-key")
	t.Setenv("MOOX_COLLECTOR_STORAGE_RPC_HMAC_KEY_FILE", "/nonexistent/obsolete-key")
	cfg, err := Load(writeCollectorConfig(t, "gateway_client:\n  key_id: admin-assigned-collector-key\n"))
	require.NoError(t, err)
	require.Equal(t, "collector", cfg.GatewayClient.Caller)
	require.Equal(t, "admin-assigned-collector-key", cfg.GatewayClient.KeyID)
	require.Equal(t, "../../secrets/caller-collector.key", cfg.GatewayClient.KeyFile)
	for _, raw := range []string{
		"gateway_client: {caller: monitor}\n",
		"gateway_client: {key_id: 'bad key'}\n",
		"gateway_client: {target: 'ip://127.0.0.1:11003'}\n",
		"gateway_client: {key_file: ''}\n",
		"storage: {key_id: old}\n",
		"storage: {hmac_key_file: old}\n",
		"gateway_client: {caller: collector, caller: monitor}\n",
		"{}\n---\n{}\n",
	} {
		_, err := Load(writeCollectorConfig(t, raw))
		require.Error(t, err, raw)
	}
	_, err = Default().OpenGateway(nil)
	require.Error(t, err, "an in-memory default must not manufacture a deployment identity")
}

func TestLoadSubjectSyncFromCollectorConfig(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "subject_sync: {fetch_timeout: 3m, attributes: [{space_id: crypto, sources: [' BINANCE '], cron: '10 8 * * *', timezone: Asia/Shanghai}]}"))
	require.NoError(t, err)
	require.Equal(t, 3*time.Minute, cfg.SubjectSync.FetchTimeout)
	require.Equal(t, []string{"binance"}, cfg.SubjectSync.Attributes[0].Sources)
	defaults, err := Load(writeCollectorConfig(t, "{}"))
	require.NoError(t, err)
	require.Len(t, defaults.SubjectSync.Attributes, 2)
	for _, job := range defaults.SubjectSync.Attributes {
		require.Equal(t, "10 8 * * *", job.Cron)
		require.Equal(t, "Asia/Shanghai", job.Timezone)
	}
	for _, raw := range []string{
		"subject_sync: {fetch_timeout: 0s}", "subject_sync: {fetch_timeout: 11m}",
		"subject_sync: {poll_interval: 1m}", "subject_sync: {health_addr: ':11413'}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [binance], cron: bad}]}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [binance], cron: '0 0 31 2 *'}]}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [binance], cron: '@every 2m'}]}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [binance], timezone: Invalid/Zone}]}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [' ']}]}",
		"subject_sync: {attributes: [{space_id: crypto, sources: [binance, BINANCE]}]}",
		"subject_sync: {attributes: [{space_id: '', sources: [binance]}]}",
	} {
		_, err = Load(writeCollectorConfig(t, raw))
		require.Error(t, err, raw)
	}
}

func TestSCFPlanConfigValidation(t *testing.T) {
	valid := func() *Config {
		cfg := Default()
		cfg.SCFRegions = map[string]marketfetch.PlanNetwork{"ap-nanjing": {VPCID: "vpc-1", SubnetID: "subnet-1"}}
		cfg.SCFPlan.AccessRoutes = map[string]SCFAccessEndpoint{"ap-nanjing": {Address: "10.0.0.5:11004", InstanceID: "access@storage"}}
		cfg.SCFMarkets = map[string]map[string][]string{"crypto": {"spot": {"ap-nanjing"}}}
		cfg.SCFPlan.CloudAccountID, cfg.SCFPlan.PackageID, cfg.SCFPlan.BaseEnvironmentFile = "acct", "pkg", "scf-env.json"
		return cfg
	}
	require.NoError(t, valid().validateSCFPlan())
	require.Equal(t, []string{"crypto"}, valid().fetchPlanSpaces())
	multi := valid()
	multi.SCFMarkets["stockcn"] = map[string][]string{"equity": {"ap-nanjing"}}
	require.Equal(t, []string{"crypto", "stockcn"}, multi.fetchPlanSpaces(), "空间集合以 scf_markets 为准，与旧调度的空间环境变量无关")
	onlyStock := valid()
	onlyStock.SCFMarkets = map[string]map[string][]string{"stockcn": {"equity": {"ap-nanjing"}}}
	require.Equal(t, []string{"stockcn"}, onlyStock.fetchPlanSpaces())
	require.Equal(t, []string{"crypto", "stockcn"}, withStoredSpaces([]string{"stockcn"}, []string{"crypto", "", "STOCKCN"}))

	noRoute := valid()
	noRoute.SCFPlan.AccessRoutes = nil
	require.ErrorContains(t, noRoute.validateSCFPlan(), "same-region Access route")
	noNetwork := valid()
	noNetwork.SCFRegions = nil
	require.ErrorContains(t, noNetwork.validateSCFPlan(), "VPC and subnet")
	noDeploy := valid()
	noDeploy.SCFPlan.PackageID = ""
	require.ErrorContains(t, noDeploy.validateSCFPlan(), "package_id")
	require.NoError(t, Default().validateSCFPlan(), "spaces without scf_markets keep the Invoke scheduler only")

	shared := valid()
	shared.SCFMarkets["stockcn"] = map[string][]string{"stock": {"ap-nanjing"}}
	require.ErrorContains(t, shared.validateSCFPlan(), "share code package")
	own := valid()
	own.SCFMarkets["stockcn"] = map[string][]string{"stock": {"ap-nanjing"}}
	own.SCFPlan.Spaces = map[string]SCFPlanSpaceConfig{"stockcn": {PackageID: "pkg-stockcn"}}
	require.NoError(t, own.validateSCFPlan())
}

func TestFetchPlanDeployForcesTheSpaceIdentity(t *testing.T) {
	t.Setenv(storageAuthSecretEnv, "test-storage-primary-secret")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"MOOX_SPACE_ID":"crypto","OTHER":"x"}`), 0o600))
	cfg := Default()
	cfg.sourcePath = filepath.Join(dir, "app.yaml")
	cfg.SCFPlan.CloudAccountID, cfg.SCFPlan.PackageID, cfg.SCFPlan.BaseEnvironmentFile = "acct", "pkg", "env.json"
	cfg.SCFPlan.Spaces = map[string]SCFPlanSpaceConfig{"stockcn": {PackageID: "pkg-stockcn"}}
	deploy, err := cfg.fetchPlanDeploy("stockcn")
	require.NoError(t, err)
	require.Equal(t, "stockcn", deploy.BaseEnvironment["MOOX_SPACE_ID"])
	require.Equal(t, "pkg-stockcn", deploy.PackageID)
	cryptoDeploy, err := cfg.fetchPlanDeploy("crypto")
	require.NoError(t, err)
	require.Equal(t, "crypto", cryptoDeploy.BaseEnvironment["MOOX_SPACE_ID"])
	require.Equal(t, "pkg", cryptoDeploy.PackageID)
}

func TestFetchPlanDeployBuildsTheBaseEnvironmentWithoutAFile(t *testing.T) {
	t.Setenv(storageAuthSecretEnv, "test-storage-primary-secret")
	cfg := Default()
	cfg.sourcePath = filepath.Join(t.TempDir(), "app.yaml")
	cfg.SCFPlan.CloudAccountID, cfg.SCFPlan.PackageName = "acct", "moox-collector-crypto-plan"
	deploy, err := cfg.fetchPlanDeploy("crypto")
	require.NoError(t, err)
	require.Equal(t, "crypto", deploy.BaseEnvironment["MOOX_SPACE_ID"])
	require.Equal(t, "moox-collector-crypto-plan", deploy.PackageName)
	require.Empty(t, deploy.PackageID)
	// Storage 应用密钥由主密钥按应用 ID 派生，密钥本身不进入配置文件。
	var keys map[string]string
	require.NoError(t, json.Unmarshal([]byte(deploy.BaseEnvironment["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"]), &keys))
	require.Equal(t, security.HMACSHA256Hex("test-storage-primary-secret", []byte("moox-collector")), keys["moox-collector"])
	require.Len(t, keys, 1)

	// 主密钥缺失时明确报错，不下发没有密钥的函数。
	t.Setenv(storageAuthSecretEnv, "")
	_, err = cfg.fetchPlanDeploy("crypto")
	require.ErrorContains(t, err, storageAuthSecretEnv)
}

func TestSCFPlanPackageSelectionRequiresExactlyOneOfNameAndID(t *testing.T) {
	plan := SCFPlanConfig{PackageName: "pkg-name", Spaces: map[string]SCFPlanSpaceConfig{"stockcn": {PackageID: "pkg-id"}}}
	key, err := plan.packageKeyFor("crypto")
	require.NoError(t, err)
	require.Equal(t, "name:pkg-name", key)
	key, err = plan.packageKeyFor("stockcn")
	require.NoError(t, err)
	require.Equal(t, "id:pkg-id", key)

	_, err = (SCFPlanConfig{PackageName: "a", PackageID: "b"}).packageKeyFor("crypto")
	require.ErrorContains(t, err, "both")
	_, err = (SCFPlanConfig{}).packageKeyFor("crypto")
	require.ErrorContains(t, err, "needs package_name")
}

// 部署渲染出的 Timer 配置经真实 Load（含 validateSCFPlan）：覆盖 crypto、仅 stockcn、多空间与关闭状态。
const renderedTimerRegions = `scf_regions:
  ap-nanjing: {vpc_id: vpc-1, subnet_id: subnet-1}
`

func renderedTimerPlan(markets, spaces string) string {
	return renderedTimerRegions + "scf_markets:\n" + markets + `scf_plan:
  cloud_account_id: acct
  memory_mb: 128
  symbols_per_function: 50
  access_routes:
    ap-nanjing: {access_address: "10.0.0.5:11004", access_id: access@storage}
  spaces:
` + spaces
}

func TestLoadRenderedTimerConfigEndToEnd(t *testing.T) {
	t.Setenv(storageAuthSecretEnv, "test-storage-primary-secret")
	t.Run("crypto only", func(t *testing.T) {
		cfg, err := Load(writeCollectorConfig(t, renderedTimerPlan("  crypto: {spot: [ap-nanjing], swap: [ap-nanjing]}\n", "    crypto: {package_name: pkg-crypto, namespace: ns-crypto}\n")))
		require.NoError(t, err)
		require.Equal(t, []string{"crypto"}, cfg.fetchPlanSpaces())
		deploy, err := cfg.fetchPlanDeploy("crypto")
		require.NoError(t, err)
		require.Equal(t, "pkg-crypto", deploy.PackageName)
		require.Equal(t, "ns-crypto", deploy.Namespace)
		require.Equal(t, "crypto", deploy.BaseEnvironment["MOOX_SPACE_ID"])
		require.Equal(t, 50, cfg.SCFPlan.symbolsPerFunction("crypto"))
	})
	t.Run("only stockcn keeps its own defaults", func(t *testing.T) {
		cfg, err := Load(writeCollectorConfig(t, renderedTimerPlan("  stockcn: {equity: [ap-nanjing]}\n", "    stockcn: {package_name: pkg-stock, namespace: ns-stock}\n")))
		require.NoError(t, err)
		require.Equal(t, []string{"stockcn"}, cfg.fetchPlanSpaces(), "只配置 stockcn 也要启动管理器")
		require.Equal(t, 40, cfg.SCFPlan.symbolsPerFunction("stockcn"))
		require.Equal(t, 8, cfg.SCFPlan.fetchFor("stockcn").Concurrency)
		require.Equal(t, 35000, cfg.SCFPlan.fetchFor("stockcn").StaggerMS)
	})
	t.Run("several spaces", func(t *testing.T) {
		cfg, err := Load(writeCollectorConfig(t, renderedTimerPlan("  crypto: {spot: [ap-nanjing]}\n  stockcn: {equity: [ap-nanjing]}\n",
			"    crypto: {package_name: pkg-crypto, namespace: ns-crypto}\n    stockcn: {package_name: pkg-stock, namespace: ns-stock}\n")))
		require.NoError(t, err)
		require.Equal(t, []string{"crypto", "stockcn"}, cfg.fetchPlanSpaces())
	})
	t.Run("two spaces sharing one package are rejected", func(t *testing.T) {
		_, err := Load(writeCollectorConfig(t, renderedTimerPlan("  crypto: {spot: [ap-nanjing]}\n  stockcn: {equity: [ap-nanjing]}\n",
			"    crypto: {package_name: shared}\n    stockcn: {package_name: shared}\n")))
		require.ErrorContains(t, err, "share code package")
	})
	t.Run("disabled renders nothing", func(t *testing.T) {
		cfg, err := Load(writeCollectorConfig(t, "database: {path: ./c.db}\n"))
		require.NoError(t, err)
		require.Empty(t, cfg.fetchPlanSpaces())
	})
}

func TestPartialFetchOverridesInheritTheRemainingFields(t *testing.T) {
	defaults := defaultSCFPlan()
	// 只写了 rps：并发、错峰等沿用 stockcn 的默认值，不会变成零。
	plan := SCFPlanConfig{Fetch: defaults.Fetch, Spaces: map[string]SCFPlanSpaceConfig{"stockcn": {Fetch: &SCFPlanFetchConfig{RPS: 10}}}}
	plan.mergeSpaceDefaults(defaults.Spaces)
	fetch := plan.fetchFor("stockcn")
	require.Equal(t, 10, fetch.RPS)
	require.Equal(t, 8, fetch.Concurrency)
	require.Equal(t, 35000, fetch.StaggerMS)
	require.Positive(t, fetch.Concurrency)
	require.Positive(t, fetch.Burst)
	require.Positive(t, fetch.RequestTimeoutMS)

	// 没有内置默认的空间只写部分字段：从全局默认补齐。
	other := SCFPlanConfig{Fetch: defaults.Fetch, Spaces: map[string]SCFPlanSpaceConfig{"futures": {Fetch: &SCFPlanFetchConfig{Concurrency: 4}}}}
	other.mergeSpaceDefaults(defaults.Spaces)
	got := other.fetchFor("futures")
	require.Equal(t, 4, got.Concurrency)
	require.Equal(t, defaults.Fetch.RPS, got.RPS)
	require.Equal(t, defaults.Fetch.RequestTimeoutMS, got.RequestTimeoutMS)
}

func TestHedgeFetchOptionsAreOffByDefaultAndInheritedFromTheGlobalFetch(t *testing.T) {
	defaults := defaultSCFPlan()
	require.Zero(t, defaults.fetchFor("crypto").HedgeMS, "默认不对冲")

	plan := SCFPlanConfig{Fetch: defaults.Fetch, Spaces: map[string]SCFPlanSpaceConfig{"crypto": {Fetch: &SCFPlanFetchConfig{HedgeMS: 800, HedgePercent: 50, HedgeHourOnly: true}}}}
	plan.mergeSpaceDefaults(defaults.Spaces)
	fetch := plan.fetchFor("crypto")
	require.Equal(t, 800, fetch.HedgeMS)
	require.Equal(t, 50, fetch.HedgePercent)
	require.True(t, fetch.HedgeHourOnly)
	require.Equal(t, defaults.Fetch.Concurrency, fetch.Concurrency, "只写对冲项时其余拉取参数沿用全局默认")
	require.Zero(t, plan.fetchFor("stockcn").HedgeMS, "其他空间不受影响")
}

// 线上配置只写对冲三项时，其余拉取参数沿用内置默认值（解码落在默认配置上，严格解析也认这三个键）。
func TestScfPlanFetchYAMLWithOnlyHedgeKeepsDefaults(t *testing.T) {
	cfg := Default()
	decoder := yaml.NewDecoder(strings.NewReader("scf_plan:\n  fetch:\n    hedge_ms: 800\n    hedge_percent: 50\n    hedge_hour_only: true\n"))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&cfg))
	fetch := cfg.SCFPlan.fetchFor("crypto")
	require.Equal(t, 800, fetch.HedgeMS)
	require.Equal(t, 50, fetch.HedgePercent)
	require.True(t, fetch.HedgeHourOnly)
	require.Equal(t, defaultSCFPlan().Fetch.Concurrency, fetch.Concurrency)
	require.Equal(t, defaultSCFPlan().Fetch.StaggerMS, fetch.StaggerMS)
}

// CLI 渲染测试生成的真实产物（modules/cli/.../testdata/scf_plan_rendered.yaml）必须能被 Collector 真实加载并通过校验。
func TestLoadTheConfigRenderedByTheCLI(t *testing.T) {
	t.Setenv(storageAuthSecretEnv, "test-storage-primary-secret")
	raw, err := os.ReadFile("../../../cli/internal/setup/config/testdata/scf_plan_rendered.yaml")
	require.NoError(t, err)
	cfg, err := Load(writeCollectorConfig(t, string(raw)))
	require.NoError(t, err)
	require.Equal(t, []string{"crypto", "stockcn"}, cfg.fetchPlanSpaces())
	for _, space := range cfg.fetchPlanSpaces() {
		deploy, deployErr := cfg.fetchPlanDeploy(space)
		require.NoError(t, deployErr, space)
		require.NotEmpty(t, deploy.PackageName, space)
		require.Contains(t, deploy.Networks, "ap-hongkong")
	}
	require.Equal(t, 40, cfg.SCFPlan.symbolsPerFunction("stockcn"), "渲染产物里只有包名与命名空间，stockcn 默认值仍应补回")
	route, err := cfg.scfPlanAccessEnvironmentRoute("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "172.19.32.13:11004", route.Address)
}
