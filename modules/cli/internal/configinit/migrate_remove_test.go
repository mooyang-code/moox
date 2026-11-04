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
