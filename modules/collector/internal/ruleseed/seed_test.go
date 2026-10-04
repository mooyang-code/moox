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
    task_id: test-task-1
    task_name: Binance 现货 K 线 1m
    data_type: kline
    tag_ids: [binance_spot]
    enabled: true
    creator: moox-setup
    collect_params:
      frequency: 1m
`

func TestLoadTaskSeed(t *testing.T) {
	tasks, err := loadTaskSeed(strings.NewReader(validSeed))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	assert.Equal(t, "crypto", task.SpaceID)
	assert.Equal(t, "test-task-1", task.TaskID)
	assert.Equal(t, "Binance 现货 K 线 1m", task.TaskName)
	assert.Equal(t, "kline", task.DataType)
	assert.Equal(t, []string{"binance_spot"}, task.TagIDs)
	assert.Equal(t, "moox-setup", task.Creator)
	assert.True(t, task.Enabled)
	ids := taskresult.ResultIDsForTask(task.SpaceID, task.TaskID, "binance_spot", "kline", "1m")
	assert.Equal(t, ids.DatasetID, task.ResultDatasetID)
	assert.Equal(t, ids.ViewID, task.ResultViewID)
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	assert.Equal(t, ids.DatasetID, params.TargetDatasetID)
}

func TestBuiltinBinanceOneMinuteTasksShareCanonicalDataset(t *testing.T) {
	tasks, err := LoadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)

	byID := make(map[string]domain.CollectionTask, len(tasks))
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	for _, taskID := range []string{"dasftksvjhj2jom4vhd0", "dasftksvjhj2jom4vhdg"} {
		task, ok := byID[taskID]
		require.True(t, ok, "missing seeded task %s", taskID)
		assert.Equal(t, "dataset_binance_kline_1m", task.ResultDatasetID)
		assert.Equal(t, "view_binance_kline_1m", task.ResultViewID)
		params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		require.NoError(t, err)
		assert.Equal(t, "dataset_binance_kline_1m", params.TargetDatasetID)
		assert.Equal(t, "binance", params.Provider)
		if taskID == taskresult.BinanceSpotKline1mTaskID {
			assert.Equal(t, "spot", params.MarketType)
		} else {
			assert.Equal(t, "swap", params.MarketType)
		}
		assert.Empty(t, params.SeriesTag, "market series tag should be inferred from each tag")
	}

	for _, taskID := range []string{"dasftksvjhj2jom4vhe0", "dasftksvjhj2jom4vheg", "dasftksvjhj2jom4vhf0"} {
		task, ok := byID[taskID]
		require.True(t, ok, "missing seeded task %s", taskID)
		ids := taskresult.ResultIDsForTask(task.SpaceID, task.TaskID, "", task.DataType, taskResultFrequency(t, task))
		assert.Equal(t, ids.DatasetID, task.ResultDatasetID, "non-allowlisted task retains task-owned dataset")
		assert.Equal(t, ids.ViewID, task.ResultViewID, "non-allowlisted task retains task-owned view")
		params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		require.NoError(t, err)
		assert.Equal(t, ids.DatasetID, params.TargetDatasetID)
	}
}

func TestLoadTaskSeedRejectsSharedTaskRouteMismatches(t *testing.T) {
	seed := strings.Replace(validSeed, "test-task-1", taskresult.BinanceSpotKline1mTaskID, 1)
	seed = strings.Replace(seed, "    creator: moox-setup\n", "    creator: moox-setup\n    result_dataset_id: dataset_binance_kline_1m\n    result_view_id: view_binance_kline_1m\n", 1)
	seed = strings.Replace(seed, "      frequency: 1m\n", "      provider: binance\n      market_type: spot\n      frequency: 1m\n", 1)
	if _, err := Load(strings.NewReader(seed)); err != nil {
		t.Fatalf("canonical shared task route should load: %v", err)
	}

	for _, test := range []struct{ name, old, replacement string }{
		{name: "wrong market", old: "market_type: spot", replacement: "market_type: swap"},
		{name: "wrong provider", old: "provider: binance", replacement: "provider: kraken"},
		{name: "missing market", old: "market_type: spot", replacement: "market_type: \"\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := strings.Replace(seed, test.old, test.replacement, 1)
			_, err := Load(strings.NewReader(invalid))
			require.Error(t, err)
		})
	}
}

func TestLoadTaskSeedRejectsUnapprovedSharedResultTarget(t *testing.T) {
	seed := strings.Replace(validSeed,
		"    creator: moox-setup\n",
		"    creator: moox-setup\n    result_dataset_id: dataset_arbitrary\n    result_view_id: view_arbitrary\n", 1)
	_, err := Load(strings.NewReader(seed))
	require.Error(t, err)
}

func taskResultFrequency(t *testing.T, task domain.CollectionTask) string {
	t.Helper()
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	return params.Frequency
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
		"duplicate":         strings.Replace(validSeed, "tasks:\n", "tasks:\n"+strings.TrimPrefix(validSeed, "tasks:\n"), 1),
		"legacy provider":   strings.Replace(validSeed, "    tag_ids: [binance_spot]\n", "    provider: binance\n    tag_ids: [binance_spot]\n", 1),
		"caller result":     strings.Replace(validSeed, "      frequency: 1m\n", "      target_dataset_id: caller-target\n      frequency: 1m\n", 1),
		"routing in params": strings.Replace(validSeed, "      frequency: 1m\n", "      provider: binance\n      market_type: spot\n      frequency: 1m\n", 1),
		"bad frequency":     strings.Replace(validSeed, "frequency: 1m", "frequency: instant", 1),
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
