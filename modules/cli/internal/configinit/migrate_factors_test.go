package configinit

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

var commonFactorIDs = []string{"ma", "ema", "macd", "rsi", "roc", "boll", "atr", "return_std", "vwap"}

// withoutCommonFactors 去掉示例里 9 个常用因子的定义与成员，模拟加入默认因子之前的配置；
// 同时把 Bias 在一个因子任务里的成员改成 disabled，用来确认已有条目不被改动。
func withoutCommonFactors(t *testing.T) string {
	t.Helper()
	common := map[string]bool{}
	for _, id := range commonFactorIDs {
		common[id] = true
	}
	var out []string
	disabledOne := false
	for _, b := range splitBlocks(strings.Split(sampleText(t), "\n")) {
		if (b.header == "factors.definitions" || b.header == "factors.members") && b.array && common[blockValue(b, "factor_id")] {
			continue
		}
		if b.header == "factors.members" && b.array && blockValue(b, "factor_id") == "bias" && !disabledOne {
			disabledOne = true
			setBlockKey(&b, "status", `"disabled"`)
		}
		out = append(out, b.lines...)
	}
	require.True(t, disabledOne)
	return strings.Join(out, "\n")
}

func countEntries(text, header string) int {
	n := 0
	for _, b := range splitBlocks(strings.Split(text, "\n")) {
		if b.header == header && b.array {
			n++
		}
	}
	return n
}

func TestMigrateAddsMissingDefaultFactorsOnce(t *testing.T) {
	path, root, keys := migrateFixture(t, withoutCommonFactors(t))
	now := func() time.Time { return time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC) }

	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, AddDefaultFactors: true, Write: true, Now: now})
	require.NoError(t, err)
	require.True(t, report.Changed && report.Valid && report.Written)
	require.Equal(t, commonFactorIDs, report.DefaultFactors.Definitions)
	require.Len(t, report.DefaultFactors.Members, 18)
	require.Contains(t, report.DefaultFactors.Members, "dataset_dasftksvjhj2jom4vhd0/1m/ema")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(raw, keys)
	require.NoError(t, err)
	text := string(plain)
	manifest, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Len(t, manifest.Factors.Definitions, countEntries(exampleTemplate, "factors.definitions"))
	require.Len(t, manifest.Factors.Members, countEntries(exampleTemplate, "factors.members"))
	require.Contains(t, text, "file = \"common.yaml\"")
	require.NotContains(t, text, "\n\n\n", "补上的条目之间只空一行")
	// 已有成员的 status 保持原样
	disabled := 0
	for _, member := range manifest.Factors.Members {
		if member.FactorID == "bias" && member.Status == "disabled" {
			disabled++
		}
	}
	require.Equal(t, 1, disabled)
	// 新定义排在已有定义之后、成员之前
	require.Less(t, strings.Index(text, "factor_id = \"vwap\"\nfile"), strings.Index(text, "[[factors.members]]"))

	again, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, AddDefaultFactors: true, Write: true, Now: now})
	require.NoError(t, err)
	require.False(t, again.Changed, "再次执行不应有改动")
	require.Empty(t, again.DefaultFactors.Definitions)
	require.Empty(t, again.DefaultFactors.Members)
}

func TestAddDefaultFactorsRequiresFactorsSection(t *testing.T) {
	_, _, err := addDefaultFactors([]byte("[hosts.control]\naddress = \"x\"\n"))
	require.ErrorContains(t, err, "[factors]")
}
