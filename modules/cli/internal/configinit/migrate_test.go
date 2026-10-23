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

// legacyText 把当前格式的示例配置改回旧格式：因子定义带旧字段、没有中文名、海外机叫 compute-1。
func legacyText(t *testing.T) string {
	t.Helper()
	text := sampleText(t)
	require.Contains(t, text, "[hosts.overseas]")
	text = strings.ReplaceAll(text, "[hosts.overseas]", "[hosts.compute-1]")
	text = regexp.MustCompile(`(?m)^overseas = `).ReplaceAllString(text, "compute-1 = ")
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

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "compute-1", RenameTo: "overseas"})
	require.NoError(t, err)
	require.True(t, report.Encrypted && report.Changed && report.Valid)
	require.False(t, report.Written)
	require.Len(t, report.Factors, 4)
	require.Equal(t, FactorMigration{FactorID: "Bias", File: "xbx.yaml", RemovedKeys: []string{"factor_type", "lookback_periods", "name", "outputs", "params_json"}}, report.Factors[0])
	require.Equal(t, []HostLabelMigration{{"control", "控制机"}, {"storage", "存储机"}, {"overseas", "海外机"}}, report.Labels)
	require.Equal(t, &HostRename{From: "compute-1", To: "overseas", Changes: 2}, report.Rename)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "预览不能改动文件")
}

func TestMigrateWritesBackupAndReencryptsCurrentFormat(t *testing.T) {
	path, root, keys := migrateFixture(t, legacyText(t))
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	now := func() time.Time { return time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC) }

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "compute-1", RenameTo: "overseas", Write: true, Now: now})
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
	require.Equal(t, []string{"trade", "access", "egress-proxy", "collector-stream"}, manifest.Placements["overseas"])
	require.NotContains(t, string(plain), "compute-1")
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
	for _, rename := range [][2]string{{"nope", "overseas"}, {"compute-1", "storage"}, {"compute-1", "Bad ID"}} {
		_, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: rename[0], RenameTo: rename[1]})
		require.Error(t, err, rename)
	}
	// 不改名时 compute-1 不在命名表里，不补中文名，但仍能通过校验。
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

// 旧组件身份 cloudnode 改名为 cloudfunction 之后，[placements] 里的旧名字会让所有命令拒绝配置；migrate 把它改成新名字。
func TestMigrateRenamesLegacyPlacementComponent(t *testing.T) {
	text := legacyText(t)
	require.Contains(t, text, `"cloudfunction"`)
	text = strings.Replace(text, `"cloudfunction"`, `"cloudnode"`, 1)
	path, root, keys := migrateFixture(t, text)

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RenameFrom: "compute-1", RenameTo: "overseas", Write: true})
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

// 单引号字符串与多行数组里的旧组件身份同样改名，注释行不动。
func TestRenameLegacyPlacementComponentHandlesQuotesAndMultilineArrays(t *testing.T) {
	blocks := splitBlocks(strings.Split(strings.Join([]string{
		"[placements]",
		"# 旧名字 \"cloudnode\" 只在注释里，不动",
		"control = ['admin', 'cloudnode']",
		"storage = [",
		"  \"access\",",
		"  \"cloudnode\",",
		"]",
		"",
	}, "\n"), "\n"))
	var report MigrateReport
	renamePlacementComponents(blocks, &report)
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
