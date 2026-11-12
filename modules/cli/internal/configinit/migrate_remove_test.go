package configinit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const removeFixture = `[factors]
enabled = true

[[factors.definitions]]
factor_id = "ema"
file = "common.yaml"

[[factors.definitions]]
factor_id = "f1_po"
file = "f1.yaml"

[[factors.tasks]]
id = "keep"
members = [
  { factor = "ema" },
]

[[factors.tasks]]
id = "drop"
members = [
  { factor = "f1_po", params = { long_span = 145, short_span = 49 } },
  { factor = "ema" },
]

[storage_retention]
x = 1
`

func TestRemoveFactorEntries(t *testing.T) {
	var report MigrateReport
	out, err := removeFactorEntries([]byte(removeFixture), []string{"drop"}, []string{"f1_po"}, &report)
	require.NoError(t, err)
	text := string(out)
	require.NotContains(t, text, "f1_po")
	require.NotContains(t, text, `id = "drop"`)
	require.Contains(t, text, `id = "keep"`)
	require.Contains(t, text, `factor_id = "ema"`)
	require.True(t, strings.HasSuffix(strings.TrimSpace(text), "x = 1"))
	require.Equal(t, []string{"drop"}, report.RemovedFactorTasks)
	require.Equal(t, []string{"f1_po"}, report.RemovedFactors)
}

func TestRemoveFactorEntriesRefusesFactorStillUsed(t *testing.T) {
	_, err := removeFactorEntries([]byte(removeFixture), nil, []string{"f1_po"}, &MigrateReport{})
	require.ErrorContains(t, err, "drop")
	_, err = removeFactorEntries([]byte(removeFixture), []string{"missing"}, nil, &MigrateReport{})
	require.ErrorContains(t, err, "missing")
}

const addExample = `[[factors.tasks]]
id = "keep"
members = [{ factor = "ema" }]

# A 股说明注释
[[factors.tasks]]
id = "ftask_cn_5m"
space_id = "stockcn"
members = [
  { factor = "ma", params = { window = 5 } },
]

[[factors.definitions]]
factor_id = "ema"
`

func TestAddFactorTasksFromExample(t *testing.T) {
	plain := "[factors]\nenabled = true\n\n[[factors.tasks]]\nid = \"keep\"\n\n[[factors.definitions]]\nfactor_id = \"ma\"\n"
	var report MigrateReport
	out, err := addFactorTasksFromExample([]byte(plain), []byte(addExample), []string{"ftask_cn_5m"}, &report)
	require.NoError(t, err)
	text := string(out)
	require.Contains(t, text, `id = "ftask_cn_5m"`)
	require.Contains(t, text, `{ factor = "ma", params = { window = 5 } },`)
	require.Less(t, strings.Index(text, `id = "keep"`), strings.Index(text, `id = "ftask_cn_5m"`))
	require.Less(t, strings.Index(text, `id = "ftask_cn_5m"`), strings.Index(text, `factor_id = "ma"`), "插在最后一个任务之后、定义之前")
	require.Equal(t, []string{"ftask_cn_5m"}, report.AddedFactorTasks)

	_, err = addFactorTasksFromExample([]byte(plain), []byte(addExample), []string{"keep"}, &MigrateReport{})
	require.ErrorContains(t, err, "已有")
	_, err = addFactorTasksFromExample([]byte(plain), []byte(addExample), []string{"missing"}, &MigrateReport{})
	require.ErrorContains(t, err, "没有")
}

// 任务成员用到、配置里没有的因子定义随任务一起从样例复制；已有的定义不重复。
func TestAddFactorTasksCopiesMissingDefinitions(t *testing.T) {
	example := "[[factors.tasks]]\nid = \"daily\"\nmembers = [\n  { factor = \"ma\" },\n  { factor = \"mom_adj\" },\n]\n\n" +
		"[[factors.definitions]]\nfactor_id = \"mom_adj\"\nfile = \"stockcn_daily.yaml\"\n\n[[factors.definitions]]\nfactor_id = \"ma\"\nfile = \"other.yaml\"\n"
	plain := "[factors]\nenabled = true\n\n[[factors.tasks]]\nid = \"keep\"\n\n[[factors.definitions]]\nfactor_id = \"ma\"\nfile = \"common.yaml\"\n\n[admin]\nusername = \"a\"\n"
	var report MigrateReport
	out, err := addFactorTasksFromExample([]byte(plain), []byte(example), []string{"daily"}, &report)
	require.NoError(t, err)
	text := string(out)
	require.Equal(t, []string{"mom_adj"}, report.AddedFactors)
	require.Contains(t, text, "[[factors.definitions]]\nfactor_id = \"mom_adj\"\nfile = \"stockcn_daily.yaml\"\n")
	require.NotContains(t, text, "other.yaml", "已有的定义不覆盖")
	require.Less(t, strings.Index(text, `id = "daily"`), strings.Index(text, `factor_id = "ma"`))
	require.Less(t, strings.Index(text, `file = "common.yaml"`), strings.Index(text, `factor_id = "mom_adj"`), "接在最后一个定义之后")
	require.Less(t, strings.Index(text, `factor_id = "mom_adj"`), strings.Index(text, "[admin]"))
}
