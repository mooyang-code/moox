package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

const validConfig = `[eventbus]
port = 4222

[hosts.control]
address = "192.0.2.10"
region = "ap-nanjing"
ssh = { port = 22, username = "ubuntu", private_key_file = "~/.ssh/moox_ed25519" }

[deployments]
control = ["admin", "eventbus"]
`

const validSecrets = `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"

[notification]
channel_type = "wecom"
webhook_url = ""
`

func TestParseRepositoryExample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "moox.toml.example"))
	require.NoError(t, err)
	cfg, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/data/moox", cfg.Paths.DeployRoot)
	assert.Equal(t, "control", cfg.ControlHost().Name)
	assert.Equal(t, "控制机", cfg.ControlHost().Label)
	assert.Equal(t, "ap-guangzhou", cfg.CloudRegion())
	assert.Equal(t, "storage", cfg.StorageHost().Name)
	assert.Equal(t, "storage", cfg.EventBusHost().Name)
	assert.True(t, cfg.HasEgressProxy())
	assert.Equal(t, "outbound", cfg.HostByID("factor").Reachability)
	// 外部入口：存储机、海外机开，控制机、因子机不开；默认入口是存储机。
	assert.True(t, cfg.HostByID("storage").ExternalEntry)
	assert.True(t, cfg.HostByID("overseas").ExternalEntry)
	assert.False(t, cfg.HostByID("control").ExternalEntry)
	assert.False(t, cfg.HostByID("factor").ExternalEntry)
	assert.Equal(t, "storage", cfg.EntryHost().Name)
	topology, err := cfg.Topology()
	require.NoError(t, err)
	for _, host := range topology.Hosts {
		assert.Equal(t, host.ID == "storage" || host.ID == "overseas", host.ExternalEntry, host.ID)
	}
	assert.Empty(t, cfg.Overrides)
	assert.Equal(t, int64(DefaultLogRetentionDays), cfg.Logging.RetentionDays)
}

func TestParseDefaultsAndHostProjection(t *testing.T) {
	cfg, err := Parse([]byte(strings.Replace(validConfig, "[eventbus]\nport = 4222\n", "", 1)))
	require.NoError(t, err)
	assert.Equal(t, DefaultDeployRoot, cfg.Paths.DeployRoot)
	assert.Equal(t, 4222, cfg.EventBus.Port)
	assert.Equal(t, "ap-nanjing", cfg.CloudRegion(), "腾讯云的默认地域取控制机的 region")
	host := cfg.ControlHost()
	require.Len(t, host.IdentityFiles(), 1)
	assert.True(t, strings.HasSuffix(host.IdentityFiles()[0], filepath.Join(".ssh", "moox_ed25519")))
	assert.False(t, strings.HasPrefix(host.IdentityFiles()[0], "~"))
	assert.Equal(t, []string{"*.binance.com", "data-api.binance.vision", "openapi.okx.com"}, cfg.EgressProxy.HTTPDomains)
	assert.Equal(t, "1m", cfg.CollectorRetention.MaintenanceInterval)
	assert.NotEmpty(t, cfg.StorageRetention.Defaults)

	noKey, err := Parse([]byte(strings.Replace(validConfig, `, private_key_file = "~/.ssh/moox_ed25519"`, "", 1)))
	require.NoError(t, err)
	assert.Nil(t, noKey.ControlHost().IdentityFiles(), "没配私钥时用 ssh-agent 与默认私钥")
}

// 已删除或放错文件的段直接报错，并指出是哪一段、该去哪里。
func TestParseRejectsRemovedSectionsAndSaysWhich(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"只有 [[factors.tasks]]":    {validConfig + "\n[[factors.tasks]]\nid = \"ftask_x\"\nname = \"任务\"\n", []string{"[[factors.tasks]]", "factor-tasks.yaml", "factor task apply --file"}},
		"[factors]":               {validConfig + "\n[factors]\nenabled = true\n", []string{"出现了 [factors]"}},
		"[[factors.definitions]]": {validConfig + "\n[[factors.definitions]]\nfactor_id = \"ma\"\nfile = \"common.yaml\"\n", []string{"[[factors.definitions]]"}},
		"[admin]":                 {"[admin]\nusername = \"admin\"\n" + validConfig, []string{"[admin]", SecretsFileName}},
		"[tencent_cloud]":         {"[tencent_cloud]\nsecret_id = \"x\"\n" + validConfig, []string{"[tencent_cloud]", SecretsFileName}},
		"tencent_cloud.region":    {"[tencent_cloud]\nregion = \"ap-guangzhou\"\n" + validConfig, []string{"tencent_cloud.region 已删除", "控制机"}},
		"[notification]":          {validConfig + "\n[notification]\nchannel_type = \"wecom\"\n", []string{"[notification]", SecretsFileName}},
		"[[exchange_keys]]":       {validConfig + "\n[[exchange_keys]]\nname = \"main\"\n", []string{"[[exchange_keys]]", SecretsFileName}},
		"CLS 写入密钥":                {validConfig + "\n[logging.cls]\nwriter_secret_key = \"x\"\n", []string{"logging.cls.writer_secret_key", SecretsFileName}},
		"[local_logs]":            {validConfig + "\n[local_logs]\nmax_size_mb = 50\n", []string{"[local_logs] 已删除"}},
		"[observability]":         {validConfig + "\n[observability]\ndeliver_policy = \"all\"\n", []string{"[observability] 已删除"}},
		"tls_enabled":             {strings.Replace(validConfig, "port = 4222", "port = 4222\ntls_enabled = true", 1), []string{"eventbus.tls_enabled 已删除"}},
		"ssh.password":            {strings.Replace(validConfig, `username = "ubuntu"`, `username = "ubuntu", password = "p"`, 1), []string{"hosts.control.ssh.password 已删除"}},
		"私钥口令":                    {strings.Replace(validConfig, `username = "ubuntu"`, `username = "ubuntu", private_key_passphrase = "p"`, 1), []string{"hosts.control.ssh.private_key_passphrase 已删除"}},
		"control_root":            {"[paths]\ndeploy_root = \"/data/moox\"\ncontrol_root = \"/data/moox/prod\"\n" + validConfig, []string{"paths.control_root 已删除"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
	_, err := Parse([]byte("unexpected = true\n" + validConfig))
	require.ErrorContains(t, err, "不认识的键")
}

func TestParseRejectsOldEncryptedFormat(t *testing.T) {
	restore := configcrypt.SetDefaultForTest(memoryKeys{})
	t.Cleanup(restore)
	sealed, err := configcrypt.Encrypt([]byte(validConfig), configcrypt.Default())
	require.NoError(t, err)
	_, err = Parse(sealed)
	require.ErrorContains(t, err, "旧格式")
}

func TestParseValidatesTopology(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"没有 eventbus":    {strings.Replace(validConfig, `, "eventbus"`, "", 1), "deployments must include eventbus"},
		"组件不在目录里":        {strings.Replace(validConfig, `"eventbus"`, `"eventbus", "storage-node"`, 1), "不在服务目录里"},
		"已删除的 access 组件": {strings.Replace(validConfig, `"eventbus"`, `"eventbus", "access"`, 1), "不在服务目录里"},
		"只能出站的主机开外部入口":   {strings.Replace(validConfig, `region = "ap-nanjing"`, "reachability = \"outbound\"\nexternal_entry = true", 1), "external_entry"},
		"主机级组件":          {strings.Replace(validConfig, `"eventbus"`, `"eventbus", "host-gateway"`, 1), "由系统自动部署"},
		"部署根是相对路径":       {"[paths]\ndeploy_root = \"data\"\n" + validConfig, "paths.deploy_root"},
		"CLS 缺主题":        {validConfig + "\n[logging.cls]\nenabled = true\nendpoint = \"ap-guangzhou.cls.tencentcs.com\"\n", "logging.cls.topic_id"},
		"Collector 维护":   {validConfig + "\n[collector_retention]\nmaintenance_offset = \"2m\"\n", "maintenance_offset"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestOverridesAreValidatedButNotApplied(t *testing.T) {
	cfg, err := Parse([]byte(validConfig + `
[overrides."factor-engine"."engine.yaml"]
"window.lookback" = 50000
"compute.max_memory" = "8GiB"
"compute.enabled" = true
`))
	require.NoError(t, err)
	assert.Equal(t, int64(50000), cfg.Overrides["factor-engine"]["engine.yaml"]["window.lookback"])
	assert.Equal(t, "8GiB", cfg.Overrides["factor-engine"]["engine.yaml"]["compute.max_memory"])

	for name, tc := range map[string]struct{ body, want string }{
		"组件不在目录里": {"[overrides.\"no-such\".\"app.yaml\"]\n\"a\" = 1\n", "组件不在服务目录里"},
		"文件名带目录":  {"[overrides.\"collector\".\"config/app.yaml\"]\n\"a\" = 1\n", "只写文件名"},
		"键路径有空段":  {"[overrides.\"collector\".\"app.yaml\"]\n\"a..b\" = 1\n", "键路径"},
		"值是数组":    {"[overrides.\"collector\".\"app.yaml\"]\n\"a\" = [1, 2]\n", "值只能是"},
		// 少写一层：解析库会把标量解成 nil 的表，也不报没解析的键，必须由校验拦下，否则调参悄悄不生效。
		"少写配置文件名": {"[overrides.\"factor-engine\"]\n\"window.lookback\" = 50000\n", "少写了配置文件名这一层"},
		"少写组件表":   {"[overrides]\n\"factor-engine\" = 1\n", "不是表"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(validConfig + "\n" + tc.body))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadAnyPathAndSecretsBeside(t *testing.T) {
	restore := configcrypt.SetDefaultForTest(memoryKeys{})
	t.Cleanup(restore)
	dir := filepath.Join(t.TempDir(), "deploy", "prod")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	configPath := filepath.Join(dir, "site.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(validConfig), 0o644))
	cfg, err := Load(configPath)
	require.NoError(t, err, "明文的 moox.toml 不要求 0600，可以放在任意路径")
	assert.Equal(t, filepath.Join(dir, SecretsFileName), cfg.SecretsPath())

	_, err = LoadSecrets(configPath)
	require.ErrorContains(t, err, "读不到")

	secretsPath := filepath.Join(dir, SecretsFileName)
	require.NoError(t, os.WriteFile(secretsPath, []byte(validSecrets), 0o644))
	require.NoError(t, os.Chmod(secretsPath, 0o644))
	_, err = LoadSecrets(configPath)
	require.ErrorContains(t, err, "0600")

	require.NoError(t, os.Chmod(secretsPath, 0o600))
	secrets, err := LoadSecrets(configPath)
	require.NoError(t, err, "明文保存的秘密文件也能读，config encrypt 再加密")
	assert.Equal(t, "admin", secrets.Admin.Username)

	sealed, err := configcrypt.Encrypt([]byte(validSecrets), configcrypt.Default())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(secretsPath, sealed, 0o600))
	secrets, err = LoadSecrets(configPath)
	require.NoError(t, err)
	assert.Equal(t, "secret-id", secrets.TencentCloud.SecretID)
	assert.Equal(t, "wecom", secrets.Notification.ChannelType)
	secrets.Clear()
	assert.Empty(t, secrets.Admin.Password)
	assert.Empty(t, secrets.TencentCloud.SecretKey)
}

func TestParseSecretsRules(t *testing.T) {
	_, err := ParseSecrets([]byte(validSecrets + "\n[[exchange_keys]]\nname = \"main\"\nexchange = \"okx\"\nenvironment = \"production\"\napi_key = \"k\"\nsecret_key = \"s\"\n"))
	require.ErrorContains(t, err, "passphrase")

	_, err = ParseSecrets([]byte(strings.Replace(validSecrets, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \"ap-guangzhou\"", 1)))
	require.ErrorContains(t, err, "tencent_cloud.region 已删除")

	_, err = ParseSecrets([]byte(validSecrets + "\n[hosts.control]\naddress = \"192.0.2.10\"\n"))
	require.ErrorContains(t, err, "写在 moox.toml 里")

	// 解析错误只给行号与键名，不带那一行的内容。
	_, err = ParseSecrets([]byte("[admin]\nusername = \"admin\"\npassword = super-secret-value\n"))
	require.ErrorContains(t, err, "第 3 行")
	assert.NotContains(t, err.Error(), "super-secret-value")

	secrets, err := ParseSecrets([]byte(validSecrets))
	require.NoError(t, err)
	cfg, err := Parse([]byte(validConfig + "\n[logging.cls]\nenabled = true\nendpoint = \"ap-guangzhou.cls.tencentcs.com\"\ntopic_id = \"topic\"\n"))
	require.NoError(t, err)
	require.ErrorContains(t, secrets.CheckFor(&cfg), "writer_secret_id")
	secrets.Logging.CLS = CLSWriter{WriterSecretID: "id", WriterSecretKey: "key"}
	require.NoError(t, secrets.CheckFor(&cfg))
}

// memoryKeys 是测试用的固定密钥。
type memoryKeys struct{}

var testKey = []byte("0123456789abcdef0123456789abcdef")

func (memoryKeys) Load() ([]byte, error)         { return testKey, nil }
func (memoryKeys) LoadOrCreate() ([]byte, error) { return testKey, nil }
func (memoryKeys) Where() string                 { return "测试密钥" }
