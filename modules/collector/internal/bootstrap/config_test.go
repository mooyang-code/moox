package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadSCFRegionBlacklists(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "scf_region_blacklists:\n  crypto: [ap-guangzhou]\n  stockcn: [ap-tokyo]\n"))
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"crypto": {"ap-guangzhou"}, "stockcn": {"ap-tokyo"}}, cfg.SCFRegionBlacklists)
}

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

	path := writeCollectorConfig(t, `
database:
  path: ./original/collector.db
storage:
  gateway_target: ip://127.0.0.1:20100
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/collector.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16012", cfg.Health.Addr)
	assert.Equal(t, "ip://127.0.0.1:30100", cfg.Storage.GatewayTarget)
}

func TestCollectorRuntimeGatewayConfigIsExplicitPair(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_RUNTIME_GATEWAY_TARGET", "")
	t.Setenv("MOOX_COLLECTOR_RUNTIME_NODE_ID", "")
	cfg, err := Load(writeCollectorConfig(t, `collector_runtime:
  gateway_target: ip://10.0.0.5:11003
  node_id: collector-2
`))
	require.NoError(t, err)
	assert.Equal(t, "ip://10.0.0.5:11003", cfg.CollectorRuntime.GatewayTarget)
	assert.Equal(t, "collector-2", cfg.CollectorRuntime.NodeID)

	t.Setenv("MOOX_COLLECTOR_RUNTIME_GATEWAY_TARGET", "collector-gw.example.com:11003")
	t.Setenv("MOOX_COLLECTOR_RUNTIME_NODE_ID", "collector-3")
	cfg, err = Load(writeCollectorConfig(t, "database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	assert.Equal(t, "collector-gw.example.com:11003", cfg.CollectorRuntime.GatewayTarget)
	assert.Equal(t, "collector-3", cfg.CollectorRuntime.NodeID)
}

func TestLoadRejectsPartialCollectorRuntimeGatewayConfig(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `collector_runtime:
  gateway_target: collector-gw.example.com:11003
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector_runtime.node_id")
}

func TestCollectorServicesListenOnLoopbackTRPC(t *testing.T) {
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
	type listener struct {
		ip       string
		port     int
		protocol string
	}
	listeners := map[string]listener{}
	for _, service := range config.Server.Service {
		listeners[service.Name] = listener{service.IP, service.Port, service.Protocol}
	}
	// 与组件目录一致：两个服务都只监听本机回环地址，经主机网关以 tRPC 访问。
	assert.Equal(t, listener{"127.0.0.1", 11402, "trpc"}, listeners["trpc.moox.collector.CollectMgr"])
	assert.Equal(t, listener{"127.0.0.1", 11422, "trpc"}, listeners["trpc.moox.collector.MarketFetchRuntime"])
	for name := range listeners {
		assert.NotContains(t, name, ".http", "HTTP 入口已经删除")
	}
}

func TestStockCNTargetDataTimeValidatorUsesConfiguredCalendar(t *testing.T) {
	calendar, err := stockmarket.LoadCalendar(filepath.Join("..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	require.NoError(t, err)
	current := time.Date(2026, 9, 30, 2, 1, 10, 0, time.UTC)
	valid := stockCNTargetDataTimeValidator(calendar, 10*time.Second, func() time.Time { return current })
	assert.True(t, valid("1m", time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)), "latest closed session minute")
	assert.False(t, valid("1m", time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)), "lunch period")
	current = time.Date(2026, 10, 1, 2, 1, 10, 0, time.UTC)
	assert.False(t, valid("1m", time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)), "configured holiday")
	assert.False(t, valid("5m", time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)), "non-1m frequency")
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

func TestLoadStockCNRuntimeCapacityConfig(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, `stockcn:
  expected_timer_function_count: 200
  measured_safe_group_size: 30
  stagger_start_second: 5
  stagger_window_seconds: 35
  stagger_max_starts_per_second: 6
`))
	require.NoError(t, err)
	assert.Equal(t, 200, cfg.StockCN.ExpectedTimerFunctionCount)
	assert.Equal(t, 30, cfg.StockCN.MeasuredSafeGroupSize)
	assert.Equal(t, 5, cfg.StockCN.StaggerStartSecond)
	assert.Equal(t, 35, cfg.StockCN.StaggerWindowSeconds)
	assert.Equal(t, 6, cfg.StockCN.StaggerMaxStartsPerSecond)
}

func TestLoadCollectorRetentionDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "collector_retention:\n  max_rows_per_pass: 25000\n  terminal_retry_retention: 336h\n"))
	require.NoError(t, err)
	assert.Equal(t, "1m", cfg.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "35s", cfg.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "20s", cfg.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 25000, cfg.CollectorRetention.MaxRowsPerPass)
	assert.Equal(t, "6h", cfg.CollectorRetention.ExecutionDetailRetention)
	assert.Equal(t, "720h", cfg.CollectorRetention.ScheduledRunSummaryRetention)
	assert.Equal(t, "336h", cfg.CollectorRetention.TerminalRetryRetention)
	assert.Equal(t, "720h", cfg.CollectorRetention.PeriodSnapshotRetention)
}

func TestLoadRejectsInvalidCollectorRetention(t *testing.T) {
	for _, body := range []string{
		"collector_retention:\n  max_rows_per_pass: 0\n",
		"collector_retention:\n  max_rows_per_pass: 8\n",
		"collector_retention:\n  max_rows_per_pass: 50001\n",
		"collector_retention:\n  maintenance_interval: 1m\n  maintenance_timeout: 2m\n",
		// A pass must end before the next minute boundary owned by the market tick.
		"collector_retention:\n  maintenance_offset: 35s\n  maintenance_timeout: 30s\n",
		"collector_retention:\n  maintenance_offset: 1m\n",
		"collector_retention:\n  maintenance_offset: -1s\n",
		"collector_retention:\n  execution_detail_retention: 9000h\n",
	} {
		_, err := Load(writeCollectorConfig(t, body))
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), "collector_retention")
	}
}

func TestLoadAcceptsMinimumCollectorRetentionBudget(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, "collector_retention:\n  max_rows_per_pass: 9\n"))
	require.NoError(t, err)
	assert.Equal(t, 9, cfg.CollectorRetention.MaxRowsPerPass)
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

func TestLoadDNSResolverConfigAndEnvironment(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_ENABLED", "true")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_DOMAINS", "fapi.binance.com,api.binance.com")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_REFRESH_INTERVAL", "5m")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_TIMEOUT", "3s")

	cfg, err := Load(writeCollectorConfig(t, "database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	assert.True(t, cfg.DNSResolver.Enabled)
	assert.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, cfg.DNSResolver.Domains)
}

func TestLoadRequiresGatewayClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  path: ./collector.db\n"), 0o644))
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway_client")
}

// testGatewayClientYAML 是测试配置共用的 gateway_client 段。
const testGatewayClientYAML = `gateway_client:
  mode: local
  caller: collector
  key_file: caller-collector.key
  ca_file: moox-ca.crt
  cache_dir: ./data/gatewayclient
`

func writeCollectorConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testGatewayClientYAML+content), 0o644))
	return path
}
