package ruleseed

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validSeed = `tasks:
  - space_id: crypto
    task_id: builtin-binance-spot-kline-1m
    task_name: Binance 现货 K 线 1m
    data_type: kline
    provider: binance
    market_type: spot
    enabled: true
    creator: moox-setup
    collect_params:
      provider: binance
      market_type: spot
      symbol_source: dataset
      symbol_dataset_id: dataset_binance_spot_symbols
      frequency: 1m
`

func TestLoadTaskSeed(t *testing.T) {
	tasks, err := loadTaskSeed(strings.NewReader(validSeed))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	assert.Equal(t, "crypto", task.SpaceID)
	assert.Equal(t, "builtin-binance-spot-kline-1m", task.TaskID)
	assert.Equal(t, "Binance 现货 K 线 1m", task.TaskName)
	assert.Equal(t, "kline", task.DataType)
	assert.Equal(t, "binance", task.Provider)
	assert.Equal(t, "spot", task.MarketType)
	assert.Equal(t, "moox-setup", task.Creator)
	assert.True(t, task.Enabled)
	ids := taskresult.ResultIDs(task.SpaceID, task.TaskID)
	assert.Equal(t, ids.DatasetID, task.ResultDatasetID)
	assert.Equal(t, ids.ViewID, task.ResultViewID)
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)
	assert.Equal(t, ids.DatasetID, params.TargetDatasetID)
}

func TestLoadTaskSeedRejectsUnknownField(t *testing.T) {
	_, err := loadTaskSeed(strings.NewReader("tasks:\n  - task_id: r1\n    mystery: true\n"))
	require.ErrorContains(t, err, "field mystery not found")
}

func TestLoadTaskSeedRequiresTrimmedTaskName(t *testing.T) {
	missing := strings.Replace(validSeed, "    task_name: Binance 现货 K 线 1m\n", "", 1)
	blank := strings.Replace(validSeed, "    task_name: Binance 现货 K 线 1m", "    task_name: \"  \\t  \"", 1)
	tooLong := strings.Replace(validSeed, "    task_name: Binance 现货 K 线 1m", "    task_name: "+strings.Repeat("任", 81), 1)

	for name, raw := range map[string]string{"missing": missing, "blank": blank, "too long": tooLong} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTaskSeed(strings.NewReader(raw))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "task_name")
			assert.NotContains(t, err.Error(), "rule")
		})
	}

	trimmed := strings.Replace(validSeed, "task_name: Binance 现货 K 线 1m", "task_name: \"  Binance 现货 K 线 1m  \"", 1)
	tasks, err := loadTaskSeed(strings.NewReader(trimmed))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Binance 现货 K 线 1m", tasks[0].TaskName)
}

func TestLoadTaskSeedRejectsInvalidContracts(t *testing.T) {
	tests := map[string]string{
		"duplicate":       strings.Replace(validSeed, "tasks:\n", "tasks:\n"+strings.TrimPrefix(validSeed, "tasks:\n"), 1),
		"legacy exchange": strings.Replace(validSeed, "provider: binance\n", "exchange: binance\n", 1),
		"caller result":   strings.Replace(validSeed, "    symbol_source: dataset\n", "    target_dataset_id: caller-target\n    symbol_source: dataset\n", 1),
		"mismatch":        strings.Replace(validSeed, "market_type: spot\n      symbol_source", "market_type: swap\n      symbol_source", 1),
		"bad frequency":   strings.Replace(validSeed, "frequency: 1m", "frequency: instant", 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadTaskSeed(strings.NewReader(raw))
			require.Error(t, err)
		})
	}
}

func TestSeedMissingIsIdempotentAndPreservesEdits(t *testing.T) {
	mgr, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	tasks, err := loadTaskSeed(strings.NewReader(validSeed))
	require.NoError(t, err)
	ctx := context.Background()
	first, err := SeedMissing(ctx, mgr.Tasks(), tasks)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{TasksCreated: 1}, first)
	second, err := SeedMissing(ctx, mgr.Tasks(), tasks)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{TasksUnchanged: 1}, second)
	require.NoError(t, mgr.Tasks().SetEnabled(ctx, "crypto", tasks[0].TaskID, false))
	third, err := SeedMissing(ctx, mgr.Tasks(), tasks)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{TasksUnchanged: 1}, third)
	got, err := mgr.Tasks().GetByTaskID(ctx, "crypto", tasks[0].TaskID)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
}
