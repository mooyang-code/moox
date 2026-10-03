package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "./data/factor/factor.db", cfg.Database.Path)
	require.Equal(t, "ip://127.0.0.1:11003", cfg.Storage.GatewayTarget)
	require.Equal(t, []string{"nats://127.0.0.1:4222"}, cfg.EventBus.URLs)
	require.Equal(t, "~/.config/moox/eventbus/factor-eventbus.yaml", cfg.EventBus.CredentialFile)
	require.Equal(t, 10*time.Second, cfg.EventBus.FetchMaxWait)
	require.Equal(t, "python3", cfg.Python.Bin)
	require.Equal(t, "./pyworker/worker.py", cfg.Python.WorkerPath)
	require.Equal(t, "./data/factor/factors", cfg.Python.FactorsDir)
	require.Equal(t, 8, cfg.Python.Workers)
	require.Equal(t, 30*time.Second, cfg.Python.TaskTimeout)
	require.Equal(t, 100, cfg.Pipeline.ReadBatchSubjects)
	require.Equal(t, 4, cfg.Pipeline.ReadWorkers)
	require.Equal(t, 20*time.Second, cfg.Pipeline.ReadTimeout)
	require.Equal(t, 1000, cfg.Pipeline.WriteBatchRows)
	require.Equal(t, time.Minute, cfg.Pipeline.PeriodBudgetMin)
	require.Equal(t, 15*time.Minute, cfg.Pipeline.PeriodBudgetMax)
	require.Equal(t, 2000, cfg.Recalc.ChunkPeriods)
}

func TestLoadConfigRejectsInvalidBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("pipeline:\n  period_budget_min: 2m\n  period_budget_max: 1m\n"), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "period_budget_min must not exceed")
}

func TestLoadRejectsLegacyConfigSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("engine:\n  workers: 24\n"), 0o600))
	_, err := Load(path)
	require.Error(t, err)
}

func TestSourceCheckerValidatesSyntaxAndComputeContract(t *testing.T) {
	checker := sourceChecker{python: PythonConfig{Bin: "python3"}}
	path := filepath.Join(t.TempDir(), "factor.py")
	require.NoError(t, os.WriteFile(path, []byte("def compute(df, params, context):\n    return df\n"), 0o600))
	require.NoError(t, checker.CheckSource(t.Context(), domain.FactorDef{}, path))
	require.NoError(t, os.WriteFile(path, []byte("def other():\n    return 1\n"), 0o600))
	require.Error(t, checker.CheckSource(t.Context(), domain.FactorDef{}, path))
	require.NoError(t, os.WriteFile(path, []byte("def compute(:\n"), 0o600))
	require.Error(t, checker.CheckSource(t.Context(), domain.FactorDef{}, path))
}
