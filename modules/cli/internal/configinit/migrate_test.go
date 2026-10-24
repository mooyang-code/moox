package configinit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

// legacyText 把当前格式的示例配置改回旧格式：因子定义带旧字段、没有中文名、海外机叫 legacy-1。
func legacyText(t *testing.T) string {
	t.Helper()
	text := sampleText(t)
	require.Contains(t, text, "[hosts.overseas]")
	text = strings.ReplaceAll(text, "[hosts.overseas]", "[hosts.legacy-1]")
	text = regexp.MustCompile(`(?m)^overseas = `).ReplaceAllString(text, "legacy-1 = ")
	text = regexp.MustCompile(`(?m)^label = .*\n`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(?m)^factor_id = "(\w+)"\nfile = "xbx.yaml"\n`).ReplaceAllString(text,
		"factor_id = \"$1\"\nfactor_type = \"timeseries\"\nfile = \"$1.py\"\nname = \"$1\"\noutputs = [\n  \"x_5\",\n  \"x_20\",\n]\nparams_json = '{\"window\":20}'\nlookback_periods = 20\n")
	_, err := setupconfig.Parse([]byte(text))
	require.ErrorContains(t, err, "no longer supported", "夹具必须是旧格式")
	return text
}

func migrateFixture(t *testing.T, text string) (path, root string, keys *configcrypt.MemoryStore) {
	t.Helper()
	root = t.TempDir()
	library := filepath.Join(root, "modules", "factor", "factors")
	require.NoError(t, os.MkdirAll(library, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(library, "xbx.yaml"), []byte("- factor_id: Bias\n  spec: x\n- factor_id: Cci\n  spec: x\n- factor_id: MinMax\n  spec: x\n- factor_id: QuoteVolumeMean\n  spec: x\n"), 0o644))
	keys = &configcrypt.MemoryStore{}
	sealed, err := configcrypt.Encrypt([]byte(text), keys)
	require.NoError(t, err)
	path = filepath.Join(root, "moox.toml")
	require.NoError(t, os.WriteFile(path, sealed, 0o600))
	return path, root, keys
}

func TestMigratePreviewsWithoutWritingAndReportsNoValues(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyText(t))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "legacy-1", RenameTo: "overseas"})
	require.NoError(t, err)
	require.True(t, report.Encrypted && report.Changed && report.Valid)
	require.False(t, report.Written)
	require.Len(t, report.Factors, 4)
	require.Equal(t, FactorMigration{FactorID: "Bias", File: "xbx.yaml", RemovedKeys: []string{"factor_type", "lookback_periods", "name", "outputs", "params_json"}}, report.Factors[0])
	require.Equal(t, []HostLabelMigration{{"control", "控制机"}, {"storage", "存储机"}, {"overseas", "海外机"}}, report.Labels)
	require.Equal(t, &HostRename{From: "legacy-1", To: "overseas", Changes: 2}, report.Rename)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "预览不能改动文件")
}

func TestMigrateWritesBackupAndReencryptsCurrentFormat(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyText(t))
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	now := func() time.Time { return time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC) }

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "legacy-1", RenameTo: "overseas", Write: true, Now: now})
	require.NoError(t, err)
	require.True(t, report.Written)
	require.Equal(t, path+".bak-20261018093000", report.Backup)
	backup, err := os.ReadFile(report.Backup)
	require.NoError(t, err)
	require.Equal(t, original, backup)

	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, configcrypt.IsEncrypted(sealed))
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	manifest, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Equal(t, "海外机", manifest.HostByID("overseas").Label)
	require.Equal(t, []string{"trade", "access", "egress-proxy", "collector-stream"}, manifest.Deployments["overseas"])
	require.NotContains(t, string(plain), "legacy-1")
	require.NotContains(t, string(plain), "factor_type")
	require.Contains(t, string(plain), "# 主机命名", "注释保持不动")

	// 再跑一次没有改动。
	again, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, Write: true})
	require.NoError(t, err)
	require.False(t, again.Changed || again.Written)
}

func TestMigrateRejectsUnknownFactorsAndBadRenames(t *testing.T) {
	text := legacyText(t)
	path, root, keys := migrateFixture(t, strings.Replace(text, `factor_id = "Cci"`, `factor_id = "Missing"`, 1))
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.ErrorContains(t, err, "因子库里没有因子 Missing")

	path, root, keys = migrateFixture(t, text)
	for _, rename := range [][2]string{{"nope", "overseas"}, {"legacy-1", "storage"}, {"legacy-1", "Bad ID"}} {
		_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: rename[0], RenameTo: rename[1]})
		require.Error(t, err, rename)
	}
	// 不改名时 legacy-1 不在命名表里，不补中文名，但仍能通过校验。
	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.NoError(t, err)
	require.Nil(t, report.Rename)
	require.Len(t, report.Labels, 2)
}

func TestMigrateImportsSCFPlanAndEntryHostNetwork(t *testing.T) {
	path, root, keys := migrateFixture(t, sampleText(t))
	plan := filepath.Join(root, "scf.json")
	require.NoError(t, os.WriteFile(plan, []byte(`{"cloud_account":{"account_id":"acct-test","account_name":"Test","credential_secret_id":"AKIDTEST","app_id":"1250000000","cos_region":"ap-guangzhou","cos_bucket":"moox-scf-test-1250000000"},
"memory_mb":256,"symbols_per_function":40,"assumed_egress_ips":2,
"regions":{"ap-hongkong":{"vpc_id":"vpc-test","subnet_id":"subnet-test","access_host":"overseas"}},
"hosts":{"overseas":{"region":"ap-hongkong","private_address":"10.1.2.3"}},
"spaces":[{"space_id":"crypto","markets":{"spot":["ap-hongkong"],"swap":["ap-hongkong"]},"package_name":"moox-collector-crypto-plan","namespace":"moox-crypto-plan"}]}`), 0o600))

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, SCFImport: plan, Write: true, Now: func() time.Time { return time.Unix(0, 0) }})
	require.NoError(t, err)
	require.True(t, report.Valid && report.Written)
	require.Equal(t, &SCFImportReport{Regions: []string{"ap-hongkong"}, Spaces: map[string][]string{"crypto": {"spot", "swap"}}, Hosts: []string{"overseas"}}, report.SCF)
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	manifest, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.True(t, manifest.SCFPlan.Enabled)
	require.Equal(t, 256, manifest.SCFPlan.MemoryMB)
	require.Equal(t, "acct-test", manifest.SCFPlan.CloudAccount.AccountID)
	require.Equal(t, "overseas", manifest.SCFPlan.Regions["ap-hongkong"].AccessHost)
	require.Equal(t, "10.1.2.3", manifest.HostByID("overseas").PrivateAddress)
	address, id, err := manifest.SCFPlanAccess("ap-hongkong")
	require.NoError(t, err)
	require.Equal(t, "10.1.2.3:11004", address)
	require.Equal(t, "access@overseas", id)
	require.Equal(t, 1, strings.Count(string(plain), "[scf_plan]"), "旧的 [scf_plan] 被整体替换")

	// 导入引用了不存在的主机时拒绝，不写回。
	bad := filepath.Join(root, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`{"regions":{"ap-hongkong":{"vpc_id":"v","subnet_id":"s","access_host":"nope"}},"hosts":{"nope":{"region":"ap-hongkong","private_address":"10.0.0.1"}},"spaces":[{"space_id":"crypto","markets":{"spot":["ap-hongkong"]}}]}`), 0o600))
	_, err = MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, SCFImport: bad})
	require.ErrorContains(t, err, "没有主机 nope")
}

// 旧组件身份 cloudnode 改名为 cloudfunction 之后，[deployments] 里的旧名字会让所有命令拒绝配置；migrate 把它改成新名字。
func TestMigrateRenamesLegacyDeploymentComponent(t *testing.T) {
	text := legacyText(t)
	require.Contains(t, text, `"cloudfunction"`)
	text = strings.Replace(text, `"cloudfunction"`, `"cloudnode"`, 1)
	path, root, keys := migrateFixture(t, text)

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "legacy-1", RenameTo: "overseas", Write: true})
	require.NoError(t, err)
	require.Equal(t, []ComponentRename{{From: "cloudnode", To: "cloudfunction", Changes: 1}}, report.Components)
	require.True(t, report.Valid && report.Written)
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	require.NotContains(t, string(plain), `"cloudnode"`)
	require.Contains(t, string(plain), `"cloudfunction"`)
}

// 旧的 [placements] 表改名为 [deployments]：新版解析器不再认旧表，migrate 负责转换，而且可以重复执行。
func TestMigrateRenamesLegacyPlacementsTable(t *testing.T) {
	header := regexp.MustCompile(`(?m)^\[deployments\]$`)
	text := header.ReplaceAllString(legacyText(t), "[placements]")
	require.NotRegexp(t, header, text)
	_, err := setupconfig.Parse([]byte(header.ReplaceAllString(sampleText(t), "[placements]")))
	require.ErrorContains(t, err, "[placements] 已改名为 [deployments]")
	path, root, keys := migrateFixture(t, text)

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "legacy-1", RenameTo: "overseas", Write: true})
	require.NoError(t, err)
	require.True(t, report.Deployments && report.Valid && report.Written)
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	require.Regexp(t, header, string(plain))
	require.NotRegexp(t, `(?m)^\[placements\]$`, string(plain))

	again, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.NoError(t, err)
	require.False(t, again.Changed || again.Deployments, "已转换的配置不再变化")
}

func TestMigrateRejectsBothPlacementsAndDeployments(t *testing.T) {
	text := legacyText(t) + "\n[placements]\ncontrol = [\"admin\"]\n"
	path, root, keys := migrateFixture(t, text)
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "legacy-1", RenameTo: "overseas"})
	require.ErrorContains(t, err, "同时有 [placements] 与 [deployments]")
}

// 单引号字符串与多行数组里的旧组件身份同样改名，注释行不动。
func TestRenameLegacyDeploymentComponentHandlesQuotesAndMultilineArrays(t *testing.T) {
	blocks := splitBlocks(strings.Split(strings.Join([]string{
		"[deployments]",
		"# 旧名字 \"cloudnode\" 只在注释里，不动",
		"control = ['admin', 'cloudnode']",
		"storage = [",
		"  \"access\",",
		"  \"cloudnode\",",
		"]",
		"",
	}, "\n"), "\n"))
	var report MigrateReport
	renameDeploymentComponents(blocks, &report)
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	text := strings.Join(out, "\n")
	require.Contains(t, text, "control = ['admin', 'cloudfunction']")
	require.Contains(t, text, "  \"cloudfunction\",")
	require.Contains(t, text, "# 旧名字 \"cloudnode\" 只在注释里，不动")
	require.Equal(t, []ComponentRename{{From: "cloudnode", To: "cloudfunction", Changes: 2}}, report.Components)
}

// legacyFactorText 在当前格式的示例配置上加回旧的因子机写法：[hosts.factor] 只登记登录方式，[factor_engine] 指向它。
func legacyFactorText(t *testing.T, engine string) string {
	t.Helper()
	text := sampleText(t)
	require.Contains(t, text, "\n[egress_proxy]\n")
	legacy := "[hosts.factor]\naddress = \"192.168.0.103\"\nssh = { port = 22, username = \"builder\", password = \"pw\" }\n\n" +
		"# 因子引擎所在的机器：local 表示运行 moox-cli 的这台电脑。\n[factor_engine]\nhost = \"" + engine + "\"\n\n"
	return strings.Replace(text, "[egress_proxy]\n", legacy+"[egress_proxy]\n", 1)
}

// 旧的 [factor_engine] 转成该主机的只能出站与两个外部程序部署，说明注释一并去掉；重复执行结果不变。
func TestMigrateConvertsFactorEngineIntoAnOutboundDeploymentHost(t *testing.T) {
	text := legacyFactorText(t, "factor")
	_, err := setupconfig.Parse([]byte(text))
	require.ErrorContains(t, err, "[factor_engine] 已取消", "夹具必须是旧格式")
	path, root, keys := migrateFixture(t, text)

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, Write: true})
	require.NoError(t, err)
	require.Equal(t, &FactorEngineMigration{Host: "factor"}, report.FactorEngine)
	require.True(t, report.Valid && report.Written)
	require.Contains(t, report.Labels, HostLabelMigration{Host: "factor", Label: "因子机"})

	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	require.NotContains(t, string(plain), "factor_engine")
	require.NotContains(t, string(plain), "因子引擎所在的机器")
	snapshot, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	host := snapshot.HostByID("factor")
	require.Equal(t, "outbound", host.Reachability)
	require.Equal(t, "因子机", host.Label)
	require.Equal(t, []string{"factor-engine", "archive-worker"}, snapshot.Deployments["factor"])

	again, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.NoError(t, err)
	require.False(t, again.Changed || again.FactorEngine != nil, "已转换的配置不再变化")
}

func TestMigrateKeepsExistingFactorDeploymentsAndAddsMissingOnes(t *testing.T) {
	text := legacyFactorText(t, "factor")
	text = regexp.MustCompile(`(?m)^(overseas = .*)$`).ReplaceAllString(text, "$1\nfactor = [\"factor-engine\"]")
	path, root, keys := migrateFixture(t, text)
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, Write: true})
	require.NoError(t, err)
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	snapshot, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Equal(t, []string{"factor-engine", "archive-worker"}, snapshot.Deployments["factor"])
}

func TestMigrateRejectsLocalFactorEngine(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyFactorText(t, "local"))
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.ErrorContains(t, err, "\"local\" 已不再支持")
	require.ErrorContains(t, err, "Linux")
}

func TestMigrateRejectsFactorEngineWithUnknownHost(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyFactorText(t, "missing"))
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys})
	require.ErrorContains(t, err, "没有登记在 [hosts.missing]")
}

// --add-deployment 把组件并入主机的 [deployments] 列表：保留已有组件、不重复，写回后仍是合法配置。
func TestMigrateAddsMissingDeploymentsToAHost(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyFactorText(t, "factor"))
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, Write: true})
	require.NoError(t, err)

	report, err := MigrateFile(path, MigrateOptions{
		RepoRoot: root, Keys: keys, Write: true, Now: func() time.Time { return time.Now().Add(time.Hour) },
		AddDeploymentHost: "factor", AddDeploymentComponents: []string{"factor-engine", "access"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"factor": {"access"}}, report.AddedDeployments)
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(sealed, keys)
	require.NoError(t, err)
	snapshot, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Equal(t, []string{"factor-engine", "archive-worker", "access"}, snapshot.Deployments["factor"])

	again, err := MigrateFile(path, MigrateOptions{
		RepoRoot: root, Keys: keys,
		AddDeploymentHost: "factor", AddDeploymentComponents: []string{"access"},
	})
	require.NoError(t, err)
	require.Empty(t, again.AddedDeployments)
	require.False(t, again.Changed, "重复执行不产生改动")
}

func TestMigrateAddDeploymentRejectsUnknownHostAndComponent(t *testing.T) {
	path, root, keys := migrateFixture(t, sampleText(t))
	_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, AddDeploymentHost: "nowhere", AddDeploymentComponents: []string{"access"}})
	require.ErrorContains(t, err, "没有主机 nowhere")
	_, err = MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, AddDeploymentHost: "storage", AddDeploymentComponents: []string{"no-such-component"}})
	require.Error(t, err, "未知组件由配置校验拒绝")
}
