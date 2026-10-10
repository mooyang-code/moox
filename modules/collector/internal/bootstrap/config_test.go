package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSCFAccessConfigUsesAssignedIdentityAndRegionalRoutes(t *testing.T) {
	cfg, err := Load(writeCollectorConfig(t, `scf_access:
  access_address: storage.example:11004
  access_id: access@storage
  caller: scf-collector
  key_id: assigned-scf-key-17
  key_file: ../secrets/scf.key
scf_access_routes:
  ap-guangzhou:
    access_address: 10.0.0.5:11004
    access_id: access@storage
`))
	require.NoError(t, err)
	require.Equal(t, "assigned-scf-key-17", cfg.SCFAccess.KeyID)
	require.Equal(t, "10.0.0.5:11004", cfg.SCFAccessRoutes["ap-guangzhou"].Address)
	_, err = cfg.scfAccessEnvironment("ap-guangzhou")
	require.ErrorContains(t, err, "signing key")
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
	listeners := make(map[string]struct {
		ip       string
		port     int
		protocol string
	})
	foundCollectMgr := false
	for _, service := range config.Server.Service {
		require.NotEqual(t, 11418, service.Port)
		if service.Name == "trpc.moox.collector.CollectMgr" {
			foundCollectMgr = true
			require.Equal(t, "127.0.0.1", service.IP)
			require.Equal(t, 11402, service.Port)
			require.Equal(t, "trpc", service.Protocol)
		}
		if strings.Contains(service.Name, "MarketFetchRuntime") {
			listeners[service.Protocol] = struct {
				ip       string
				port     int
				protocol string
			}{service.IP, service.Port, service.Protocol}
		}
	}
	require.True(t, foundCollectMgr)
	assert.NotContains(t, listeners, "http")
	require.Len(t, listeners, 1)
	assert.Equal(t, struct {
		ip       string
		port     int
		protocol string
	}{"127.0.0.1", 11422, "trpc"}, listeners["trpc"])
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
