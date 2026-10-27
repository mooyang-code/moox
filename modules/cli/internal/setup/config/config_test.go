package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"

	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSCFAccessInstanceIDs(t *testing.T) {
	for _, id := range []string{"access@storage", "access@regional-sg"} {
		require.True(t, validAccessInstanceID(id))
	}
	for _, id := range []string{"storage", "access@bad_node", "access@Bad", "access@"} {
		require.False(t, validAccessInstanceID(id))
	}
}

func TestValidateSCFAccessAddress(t *testing.T) {
	require.NoError(t, validateAccessAddress("storage.example:11004", "scf"))
	for _, value := range []string{"ip://storage.example:11004", "https://storage.example", "storage.example:0", "storage.example:11004/path"} {
		require.Error(t, validateAccessAddress(value, "scf"))
	}
}

const validManifest = `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"

[eventbus]
port = 4222
tls_enabled = true

[hosts.control]
address = "192.0.2.10"
[hosts.control.ssh]
port = 22
username = "ubuntu"
password = "control-password"

[notification]
channel_type = "wecom"
webhook_url = ""

[deployments]
control = ["admin", "console-proxy", "web-host", "eventbus"]
`

func writeManifest(t *testing.T, root, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, "moox.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), mode))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestLoadValidManifest(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)

	snapshot, err := Load(path, root)
	require.NoError(t, err)
	assert.Equal(t, "admin", snapshot.Manifest.Admin.Username)
	assert.Equal(t, "192.0.2.10", snapshot.Manifest.EventBus.PublicAddress)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
	assert.True(t, snapshot.Manifest.EventBus.TLSEnabled)
	assert.Equal(t, "ap-guangzhou", snapshot.Manifest.TencentCloud.Region)
	assert.Equal(t, DefaultDeployRoot, snapshot.Manifest.Paths.DeployRoot)
	assert.Equal(t, DefaultControlRoot, snapshot.Manifest.Paths.ControlRoot)
	assert.Equal(t, DefaultStorageRoot, snapshot.Manifest.Paths.StorageRoot)
	assert.Equal(t, "1m", snapshot.Manifest.StorageView.MaintenanceCheckInterval)
	assert.Equal(t, "1h", snapshot.Manifest.StorageView.CapacityCheckInterval)
	assert.Equal(t, "1h0m0s", snapshot.Manifest.StorageView.CapacityCheckJitter)
	assert.Equal(t, int64(4<<30), snapshot.Manifest.StorageView.MaxViewFileBytes)
	policy := snapshot.Manifest.StoragePolicy()
	recommended := storagepolicy.Default().Retention
	recommended.Spaces = RecommendedSpaceRetention()
	assert.Equal(t, recommended, policy.Retention, "an omitted [storage_retention] uses the recommended retention")
	assert.Equal(t, uint64(10000), policy.View.Bars)
	assert.Equal(t, uint64(12000), policy.View.TrimBars)
	require.NoError(t, policy.Validate())
	assert.Equal(t, 50, snapshot.Manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 5, snapshot.Manifest.LocalLogs.BackupCount)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost().Port)
	assert.Empty(t, snapshot.Manifest.Notification.WebhookURL)
	assert.Empty(t, snapshot.Manifest.OtherHosts())
	require.NoError(t, snapshot.VerifyUnchanged())
}

func TestLoadRoleSpecificStorageAndViewHosts(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
storage = ["storage-primary", "storage-node", "storage-view"]
[hosts.storage]
address = "192.0.2.20"
provider = "Tencent"
ssh = { username = "ubuntu", password = "storage-password" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "storage", snapshot.Manifest.StorageHost().Name)
	require.Equal(t, "storage", snapshot.Manifest.ViewHost().Name)
	require.Equal(t, snapshot.Manifest.StorageHost(), snapshot.Manifest.ViewHost())
	assert.Equal(t, "ubuntu", snapshot.Manifest.StorageHost().Username)
	assert.Equal(t, "storage-password", snapshot.Manifest.ViewHost().Password)
	assert.Equal(t, "tencent", snapshot.Manifest.StorageHost().Provider)
	assert.Len(t, snapshot.Manifest.Hosts(), 2)
}

func TestValidateRejectsStorageRootOverlapWithControl(t *testing.T) {
	paths := &Paths{
		DeployRoot:  "/data/moox",
		ControlRoot: "/data/moox/prod",
		StorageRoot: "/data/moox/prod/storage",
		ArchiveRoot: "/data/moox/archive",
	}
	require.ErrorContains(t, validatePaths(paths), "must not overlap")
	paths.StorageRoot = "/data/moox"
	require.ErrorContains(t, validatePaths(paths), "must not overlap")
	paths.StorageRoot = "/data/moox/storage"
	require.NoError(t, validatePaths(paths))
	paths.ArchiveRoot = "/data/moox/storage/archive"
	require.ErrorContains(t, validatePaths(paths), "archive_root must not overlap")
	paths.ArchiveRoot = "/data/moox"
	require.ErrorContains(t, validatePaths(paths), "archive_root must not overlap")
}

func TestLoadLocalLogRotationFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[local_logs]
max_size_mb = 128
backup_count = 7
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 128, snapshot.Manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 7, snapshot.Manifest.LocalLogs.BackupCount)
}

func TestLoadRejectsInvalidLocalLogRotation(t *testing.T) {
	for name, body := range map[string]string{
		"max size":     "[local_logs]\nmax_size_mb = 0\nbackup_count = 5\n",
		"backup count": "[local_logs]\nmax_size_mb = 50\nbackup_count = 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n"+body, 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "local_logs")
		})
	}
}

func TestLoadPathsFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/custom"
control_root = "/data/custom/control"
storage_root = "/data/custom/storage"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, Paths{DeployRoot: "/data/custom", ControlRoot: "/data/custom/control", StorageRoot: "/data/custom/storage", ArchiveRoot: "/data/custom/archive"}, snapshot.Manifest.Paths)
}

func TestLoadAllowsStorageRootOnSeparateHostMount(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/moox"
control_root = "/data/moox/prod"
storage_root = "/home/ubuntu/moox/storage"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "/home/ubuntu/moox/storage", snapshot.Manifest.Paths.StorageRoot)
}

func TestLoadRejectsPathsOutsideDeployRoot(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/moox"
control_root = "/var/lib/moox"
storage_root = "/data/moox/storage"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paths.control_root")
}

func TestLoadStorageRetentionFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[storage_retention]
view_rows_per_series = 777

[storage_retention.spaces.crypto]
"1m" = "14d"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	policy := snapshot.Manifest.StoragePolicy()
	assert.Equal(t, uint64(777), policy.View.Bars)
	assert.Equal(t, uint64(933), policy.View.TrimBars)
	period, source, err := policy.Retention.Resolve("crypto", "1m")
	require.NoError(t, err)
	assert.Equal(t, "336h", period.String())
	assert.Equal(t, storagepolicy.SourceSpace, source)
	// The omitted defaults table is the recommended one; a given spaces table
	// replaces the recommended space overrides instead of merging with them.
	period, source, err = policy.Retention.Resolve("stockcn", "1m")
	require.NoError(t, err)
	assert.Equal(t, "840h", period.String())
	assert.Equal(t, storagepolicy.SourceDefault, source)
}

func TestLoadCollectorRetentionDefaultsAndOverrides(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "1m", snapshot.Manifest.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "35s", snapshot.Manifest.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "20s", snapshot.Manifest.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 50000, snapshot.Manifest.CollectorRetention.MaxRowsPerPass)

	root = t.TempDir()
	body := validManifest + `
[collector_retention]
maintenance_interval = "2m"
maintenance_offset = "10s"
maintenance_timeout = "30s"
max_rows_per_pass = 25000
`
	snapshot, err = Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "2m", snapshot.Manifest.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "10s", snapshot.Manifest.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "30s", snapshot.Manifest.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 25000, snapshot.Manifest.CollectorRetention.MaxRowsPerPass)
}

func TestLoadRejectsInvalidCollectorRetention(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "zero row budget", body: "max_rows_per_pass = 0", want: "max_rows_per_pass"},
		{name: "excessive row budget", body: "max_rows_per_pass = 50001", want: "max_rows_per_pass"},
		{name: "timeout exceeds interval", body: `maintenance_interval = "1m"
maintenance_timeout = "2m"`, want: "maintenance_timeout"},
		{name: "pass crosses the next interval boundary", body: `maintenance_offset = "35s"
maintenance_timeout = "30s"`, want: "maintenance_timeout"},
		{name: "offset outside interval", body: `maintenance_offset = "1m"`, want: "maintenance_offset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n[collector_retention]\n"+tt.body+"\n", 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadAcceptsMinimumCollectorRetentionBudget(t *testing.T) {
	root := t.TempDir()
	_, err := Load(writeManifest(t, root, validManifest+"\n[collector_retention]\nmax_rows_per_pass = 1\n", 0o600), root)
	require.NoError(t, err)
}

func TestLoadRejectsInvalidStorageRetention(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no bars", body: "[storage_retention]\nview_rows_per_series = 0", want: "view.bars"},
		{name: "incomplete defaults", body: "[storage_retention.defaults]\n\"1m\" = \"7d\"", want: "must cover every standard frequency"},
		{name: "alias frequency", body: "[storage_retention.spaces.crypto]\n\"1H\" = \"7d\"", want: "not a canonical frequency"},
		{name: "invalid period", body: "[storage_retention.spaces.crypto]\n\"1m\" = \"7 days\"", want: "retention"},
		{name: "default retention shorter than view bars", body: "[storage_retention.defaults]\n\"30s\" = \"90h\"\n\"1m\" = \"3d\"\n\"5m\" = \"35d\"\n\"15m\" = \"105d\"\n\"30m\" = \"210d\"\n\"1h\" = \"420d\"\n\"4h\" = \"1700d\"\n\"1d\" = \"forever\"\n\"1w\" = \"forever\"\n\"1mo\" = \"forever\"", want: "fewer than view.bars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n"+tt.body+"\n", 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadRejectsInvalidStorageViewCapacitySchedule(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "nonpositive interval", body: "capacity_check_interval = \"0s\"", want: "capacity_check_interval"},
		{name: "interval over one day", body: "capacity_check_interval = \"25h\"", want: "capacity_check_interval"},
		{name: "zero jitter", body: "capacity_check_jitter = \"0s\"", want: "capacity_check_jitter"},
		{name: "nonpositive jitter", body: "capacity_check_jitter = \"-1s\"", want: "capacity_check_jitter"},
		{name: "jitter exceeds interval", body: "capacity_check_interval = \"30m\"\ncapacity_check_jitter = \"1h\"", want: "capacity_check_jitter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			body := validManifest + "\n[storage_view]\n" + tt.body + "\n"
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadStorageViewDefaultsJitterToShorterConfiguredInterval(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[storage_view]
capacity_check_interval = "30m"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "30m", snapshot.Manifest.StorageView.CapacityCheckInterval)
	require.Equal(t, "30m0s", snapshot.Manifest.StorageView.CapacityCheckJitter)
}

const factorSetupHead = `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.definitions]]
factor_id = "ema"
file = "common.yaml"

[[factors.definitions]]
factor_id = "bias"
file = "timeseries.yaml"
`

const factorSetupTask = `
[[factors.tasks]]
id = "ftask_swap5m"
name = "合约五分钟"
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_5m"
freq = "5m"
members = [
  { factor = "bias" },
  { factor = "ema" },
  { factor = "ema", params = { span = 20, warmup = 50000 } },
  { factor = "bias", var = true, status = "disabled" },
]
`

func TestFactorSetupParsesTasksWithEmbeddedMembers(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest+factorSetupHead+factorSetupTask, 0o600), root)
	require.NoError(t, err)
	factors := snapshot.Manifest.Factors
	assert.True(t, factors.Enabled)
	assert.Equal(t, "examples/factors", factors.SourceDir)
	require.Len(t, factors.Definitions, 2)
	require.Len(t, factors.Tasks, 1)
	task := factors.Tasks[0]
	assert.Equal(t, "ftask_swap5m", task.ID)
	assert.Equal(t, "合约五分钟", task.Name)
	require.Len(t, task.Members, 4)
	assert.Equal(t, "enabled", task.Members[0].Status, "状态缺省为 enabled")
	assert.Empty(t, task.Members[1].Params)
	assert.Equal(t, map[string]float64{"span": 20, "warmup": 50000}, task.Members[2].Params)
	assert.True(t, task.Members[3].Var)
	assert.Equal(t, "disabled", task.Members[3].Status)
}

// 同一个源数据集可以有多个任务。
func TestFactorSetupAllowsSeveralTasksOnOneSourceDataset(t *testing.T) {
	root := t.TempDir()
	second := strings.Replace(factorSetupTask, `ftask_swap5m`, `ftask_swap5m_b`, 1)
	second = strings.Replace(second, `合约五分钟`, `合约五分钟乙`, 1)
	snapshot, err := Load(writeManifest(t, root, validManifest+factorSetupHead+factorSetupTask+second, 0o600), root)
	require.NoError(t, err)
	assert.Len(t, snapshot.Manifest.Factors.Tasks, 2)
}

func TestFactorSetupRejectsBadTasks(t *testing.T) {
	cases := map[string]struct{ from, to, contains string }{
		"id 前缀":      {`id = "ftask_swap5m"`, `id = "swap5m"`, "factors.tasks[0].id"},
		"id 后缀字符":    {`id = "ftask_swap5m"`, `id = "ftask_Swap-5m"`, "factors.tasks[0].id"},
		"名称没有汉字":     {`name = "合约五分钟"`, `name = "swap5m"`, "must contain Chinese"},
		"名称过长":       {`name = "合约五分钟"`, `name = "合约五分钟合约五分钟合约五分钟"`, "1-10 characters"},
		"缺少数据集":      {`source_dataset_id = "dataset_crypto_kline_5m"`, ``, "requires id, name"},
		"成员引用未声明的因子": {`{ factor = "bias" },`, `{ factor = "ghost" },`, "unknown factor"},
		"成员状态非法":     {`status = "disabled"`, `status = "paused"`, "status must be"},
		"相同参数重复":     {`{ factor = "ema" },`, `{ factor = "bias" },`, "repeats factor"},
		"成员缺因子":      {`{ factor = "ema" },`, `{ params = { span = 5 } },`, "requires factor"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			body := validManifest + factorSetupHead + strings.Replace(factorSetupTask, tc.from, tc.to, 1)
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.ErrorContains(t, err, tc.contains)
		})
	}
}

func TestFactorSetupRejectsDuplicateTaskIDAndName(t *testing.T) {
	root := t.TempDir()
	_, err := Load(writeManifest(t, root, validManifest+factorSetupHead+factorSetupTask+factorSetupTask, 0o600), root)
	require.ErrorContains(t, err, "duplicated")

	other := strings.Replace(factorSetupTask, `ftask_swap5m`, `ftask_other`, 1)
	_, err = Load(writeManifest(t, root, validManifest+factorSetupHead+factorSetupTask+other, 0o600), root)
	require.ErrorContains(t, err, `name "合约五分钟" is duplicated`)
}

func TestLoadDefaultsDisableFactors(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.False(t, snapshot.Manifest.Factors.Enabled)
}

func TestLoadRejectsRetiredFactorDefinitionFields(t *testing.T) {
	for _, declaration := range []string{`factor_type = "timeseries"`, `lookback_periods = 20`, `params_json = "{}"`, `name = "bias"`} {
		root := t.TempDir()
		body := validManifest + `
[factors]
enabled = true
source_dir = "examples/factors"
[[factors.definitions]]
factor_id = "bias"
file = "timeseries.yaml"
` + declaration + "\n"
		_, err := Load(writeManifest(t, root, body, 0o600), root)
		require.ErrorContains(t, err, "accepts only factor_id and file", declaration)
	}
}

func TestLoadRejectsNonYAMLFactorFile(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "examples/factors"
[[factors.definitions]]
factor_id = "bias"
file = "bias.py"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "YAML factor library file")
}

func TestLoadNotificationWebhook(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"`, 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test", snapshot.Manifest.Notification.WebhookURL)
}

func TestLoadDefaultsEventBusPort(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "port = 4222\n", "", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
}

func TestLoadTencentCloudRegion(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \"ap-shanghai\"", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "ap-shanghai", snapshot.Manifest.TencentCloud.Region)
}

func TestLoadDefaultsHostPortsAndAllowsSSHKeyAuthentication(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `password = "control-password"`, "", 1) + `
[hosts.compute]
address = "192.0.2.11"
ssh = { username = "ubuntu" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost().Port)
	assert.Empty(t, snapshot.Manifest.ControlHost().Password)
	require.Len(t, snapshot.Manifest.OtherHosts(), 1)
	assert.Equal(t, 22, snapshot.Manifest.OtherHosts()[0].Port)
}

func TestLoadEgressProxyConfiguration(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest+`
[egress_proxy]
http_domains = ["*.binance.com", "data-api.binance.vision"]
[egress_proxy.dns]
domains = ["FAPI.BINANCE.COM.", "api.binance.com"]
`, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, snapshot.Manifest.EgressProxy.DNS.Domains)
	for name, mutate := range map[string]func(*EgressProxy){
		"duplicate domain": func(c *EgressProxy) { c.DNS.Domains = []string{"api.binance.com", "API.BINANCE.COM."} },
		"wildcard DNS":     func(c *EgressProxy) { c.DNS.Domains = []string{"*.binance.com"} },
		"invalid interval": func(c *EgressProxy) { c.DNS.RequestTimeoutMS = 0 },
		"invalid port":     func(c *EgressProxy) { c.DNS.ProbePort = 70000 },
		"invalid cap":      func(c *EgressProxy) { c.DNS.MaxIPsPerDomain = 5 },
		"too many domains": func(c *EgressProxy) { c.DNS.Domains = make([]string, 17) },
		"invalid HTTP":     func(c *EgressProxy) { c.HTTPDomains = []string{"https://binance.com"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := snapshot.Manifest.EgressProxy
			mutate(&cfg)
			require.Error(t, validateEgressProxy(&cfg))
		})
	}
}
func TestDeploymentsRejectAmbiguousOrInvalidComponents(t *testing.T) {
	m := Manifest{HostCatalog: map[string]HostDefinition{
		"control": {Address: "192.0.2.10"}, "compute": {Address: "192.0.2.11"},
	}}
	for _, deployments := range []map[string][]string{
		{"missing": {"trade"}}, {"compute": {"missing"}}, {"compute": {"trade", "trade"}},
		{"control": {"admin", "console-proxy", "web-host", "trade"}, "compute": {"trade"}},
		{"compute": {"host-gateway"}}, {"compute": {"admin"}}, {"compute": {"web-host"}},
	} {
		if _, ok := deployments["control"]; !ok {
			deployments["control"] = []string{"admin", "console-proxy", "web-host", "eventbus"}
		}
		m.Deployments = deployments
		require.Error(t, validateDeployments(&m))
	}
	m.Deployments = map[string][]string{"control": {"admin", "console-proxy", "web-host", "eventbus"}, "compute": {"trade", "egress-proxy"}}
	require.NoError(t, validateDeployments(&m))
	require.Equal(t, "compute", m.ComponentHost("trade"))
}

// [compile_host] 已退役：编译在本机进行。留着它要明确报错，提示删除，而不是静默忽略。
func TestLoadRejectsRetiredCompileHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[hosts.compile]
address = "192.0.2.20"
ssh = { username = "builder" }
[compile_host]
host = "compile"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "[compile_host] is retired")
}

func TestLoadOptionalStrategyHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
strategy = ["strategy"]
[hosts.strategy]
address = "192.0.2.21"
ssh = { username = "ubuntu" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.HasStrategyHost())
	assert.Equal(t, "strategy", snapshot.Manifest.StrategyHost().Name)
	assert.Equal(t, 22, snapshot.Manifest.StrategyHost().Port)
	require.Len(t, snapshot.Manifest.Hosts(), 2)
	assert.Equal(t, "strategy", snapshot.Manifest.Hosts()[1].Name)
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"missing admin password", strings.Replace(validManifest, `password = "admin-password"`, `password = ""`, 1), "admin.password"},
		{"bcrypt password too long", strings.Replace(validManifest, "admin-password", strings.Repeat("x", 73), 1), "72 bytes"},
		{"missing secret id", strings.Replace(validManifest, `secret_id = "secret-id"`, `secret_id = ""`, 1), "tencent_cloud.secret_id"},
		{"missing secret key", strings.Replace(validManifest, `secret_key = "secret-key"`, `secret_key = ""`, 1), "tencent_cloud.secret_key"},
		{"empty region", strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \" \"", 1), "tencent_cloud.region"},
		{"missing address", strings.Replace(validManifest, `address = "192.0.2.10"`, `address = ""`, 1), "hosts.control.address"},
		{"address with scheme", strings.ReplaceAll(validManifest, "192.0.2.10", "tls://192.0.2.10"), "hosts.control.address"},
		{"address with path", strings.ReplaceAll(validManifest, "192.0.2.10", "192.0.2.10/nats"), "hosts.control.address"},
		{"address with port", strings.ReplaceAll(validManifest, "192.0.2.10", "192.0.2.10:4222"), "hosts.control.address"},
		{"invalid private address", strings.Replace(validManifest, "[hosts.control]", "[hosts.control]\nprivate_address = \"http://private\"", 1), "hosts.control.private_address"},
		{"invalid host id", strings.ReplaceAll(validManifest, "hosts.control", "hosts.Uppercase"), "canonical host IDs"},
		{"missing SSH username", strings.Replace(validManifest, `username = "ubuntu"`, `username = ""`, 1), "hosts.control.ssh.username"},
		{"explicit zero SSH port", strings.Replace(validManifest, "port = 22", "port = 0", 1), "hosts.control.ssh.port"},
		{"invalid SSH port", strings.Replace(validManifest, "port = 22", "port = 70000", 1), "hosts.control.ssh.port"},
		{"invalid TLS mode", strings.Replace(validManifest, "[hosts.control]", "[hosts.control]\ntls_mode = \"bad\"", 1), "hosts.control.tls_mode"},
		{"eventbus explicit zero port", strings.Replace(validManifest, "port = 4222", "port = 0", 1), "eventbus.port"},
		{"eventbus invalid port", strings.Replace(validManifest, "port = 4222", "port = 70000", 1), "eventbus.port"},
		{"eventbus tls disabled", strings.Replace(validManifest, "tls_enabled = true", "tls_enabled = false", 1), "eventbus.tls_enabled"},
		{"missing eventbus deployment", strings.Replace(validManifest, `, "eventbus"`, "", 1), "deployments must include eventbus"},
		{"protected component missing", strings.Replace(validManifest, `"console-proxy", `, "", 1), "protected component"},
		{"unknown deployment", validManifest + "unknown = [\"trade\"]\n", "deployments host ID"},
		{"automatic component", strings.Replace(validManifest, `"eventbus"`, `"eventbus", "host-gateway"`, 1), "automatic component"},
		{"unknown field", "unexpected = true\n" + validManifest, "unknown field"},
		{"retired factor engine section", validManifest + "[factor_engine]\nhost = \"missing\"\n", "[factor_engine] 已取消"},
		{"duplicate address", validManifest + "[hosts.compute]\naddress = \"192.0.2.10\"\nssh = { username = \"ubuntu\" }\n", "duplicates hosts."},
	}
	for _, hook := range []string{"http://example.test/hook", "not-a-url", "https://open.feishu.cn/hook", "https://example.invalid/hook"} {
		tests = append(tests, struct{ name, body, want string }{"invalid webhook " + hook, strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "`+hook+`"`, 1), "notification.webhook_url"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, tt.body, 0o600), root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "admin-password")
			assert.NotContains(t, err.Error(), "secret-key")
		})
	}
}

func TestLoadRejectsRetiredDeploymentKeys(t *testing.T) {
	for _, section := range []string{"control_host", "storage_host", "view_host", "strategy_host", "other_hosts"} {
		t.Run(section, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"["+section+"]\nname = \"control\"\n", 0o600), root)
			require.ErrorContains(t, err, "[hosts.<host-id>]")
			require.ErrorContains(t, err, "[deployments]")
		})
	}
	t.Run("scf_fetcher", func(t *testing.T) {
		root := t.TempDir()
		_, err := Load(writeManifest(t, root, validManifest+"[scf_fetcher]\nenabled = true\n", 0o600), root)
		require.ErrorContains(t, err, "[scf_fetcher] is retired")
	})
}

func TestLoadRejectsInsecureFile(t *testing.T) {
	t.Run("wrong mode", func(t *testing.T) {
		root := t.TempDir()
		_, err := Load(writeManifest(t, root, validManifest, 0o644), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0600")
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		require.NoError(t, os.WriteFile(target, []byte(validManifest), 0o600))
		path := filepath.Join(root, "moox.toml")
		require.NoError(t, os.Symlink(target, path))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular file")
	})

	t.Run("wrong basename", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "other.toml")
		require.NoError(t, os.WriteFile(path, []byte(validManifest), 0o600))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moox.toml")
	})

	t.Run("outside repository root", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		_, err := Load(writeManifest(t, outside, validManifest, 0o600), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository root")
	})
}

func TestSnapshotDetectsMutation(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(validManifest+"\n"), 0o600))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}

func TestSnapshotDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	replacement := filepath.Join(root, "replacement")
	require.NoError(t, os.WriteFile(replacement, []byte(validManifest), 0o600))
	require.NoError(t, os.Rename(replacement, path))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}

func TestLoadDecryptsAnEncryptedManifest(t *testing.T) {
	restore := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	defer restore()
	sealed, err := configcrypt.Encrypt([]byte(validManifest), configcrypt.Default())
	require.NoError(t, err)
	root := t.TempDir()
	path := writeManifest(t, root, string(sealed), 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)
	assert.True(t, snapshot.Encrypted)
	assert.Equal(t, "admin", snapshot.Manifest.Admin.Username)
	require.NoError(t, snapshot.VerifyUnchanged())

	plain, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.False(t, plain.Encrypted, "明文配置仍然可以读取")

	tampered := []byte(strings.Replace(string(sealed), "key: ", "key: f", 1))
	_, err = Load(writeManifest(t, root, string(tampered), 0o600), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_invalid")
}

// 主机可达性：outbound 进入拓扑；未知取值拒绝；未填写视为可直达。
func TestLoadHostReachability(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[hosts.home]
address = "192.168.0.103"
reachability = " Outbound "
ssh = { username = "mooyang" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "outbound", snapshot.Manifest.HostByID("home").Reachability)
	require.Empty(t, snapshot.Manifest.ControlHost().Reachability)
	topology, err := snapshot.Manifest.Topology()
	require.NoError(t, err)
	for _, host := range topology.Hosts {
		if host.ID == "home" {
			require.Equal(t, "outbound", host.Reachability)
		}
	}
	other := t.TempDir()
	_, err = Load(writeManifest(t, other, strings.Replace(body, "Outbound", "inbound", 1), 0o600), other)
	require.ErrorContains(t, err, "reachability")
}

func TestLoadHostLabel(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "[hosts.control]\n", "[hosts.control]\nlabel = \" 控制机 \"\n", 1)
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "控制机", snapshot.Manifest.ControlHost().Label)
	for _, invalid := range []string{"控制\n机", strings.Repeat("机", 33)} {
		other := t.TempDir()
		bad := strings.Replace(validManifest, "[hosts.control]\n", "[hosts.control]\nlabel = "+strconv.Quote(invalid)+"\n", 1)
		_, err := Load(writeManifest(t, other, bad, 0o600), other)
		require.ErrorContains(t, err, "label")
	}
}
