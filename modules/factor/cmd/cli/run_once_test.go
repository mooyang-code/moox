package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

func TestRunOnceLoadsAppConfig(t *testing.T) {
	runtimeRoot := t.TempDir()
	configDir := filepath.Join(runtimeRoot, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	configPath := filepath.Join(configDir, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
database:
  path: ../data/factor/test.db
storage:
  gateway_target: ip://10.0.0.8:11003
  gateway_node_id: node-a
engine:
  python_bin: /tmp/factor-venv/bin/python
  worker_path: ./pyworker/worker.py
  factors_dir: ./factors
  workers: 4
  task_timeout_ms: 45000
scheduler:
  max_retry: 2
`), 0o644))
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "factor")
	t.Setenv("MOOX_GATEWAY_CALLER", "factor")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-secret")

	resolved, err := resolveRunOnceRuntime(cliConfig{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(filepath.Join(runtimeRoot, "../data/factor/test.db")), resolved.DBPath)
	require.Equal(t, filepath.Join(runtimeRoot, "factors"), resolved.FactorsDir)
	require.Equal(t, filepath.Join(runtimeRoot, "pyworker/worker.py"), resolved.WorkerPath)
	require.Equal(t, "/tmp/factor-venv/bin/python", resolved.PythonBin)
	require.Equal(t, "ip://10.0.0.8:11003", resolved.GatewayTarget)
	require.Equal(t, "node-a", resolved.GatewayNodeID)
	require.Equal(t, "factor", resolved.Credentials.KeyID)
	require.Equal(t, "factor", resolved.Credentials.Caller)
	require.Equal(t, "test-secret", resolved.Credentials.Secret)
	require.Equal(t, 1, resolved.Workers)
	require.Equal(t, 45*time.Second, resolved.TaskTimeout)
	require.Equal(t, 2, resolved.MaxRetry)
}

func TestRunOnceCLIOverridesAppConfig(t *testing.T) {
	runtimeRoot := t.TempDir()
	configDir := filepath.Join(runtimeRoot, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	configPath := filepath.Join(configDir, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
database:
  path: ./configured.db
engine:
  worker_path: ./configured-worker.py
  factors_dir: ./configured-factors
`), 0o644))
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET", "ip://127.0.0.2:11003")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID", "env-node")
	t.Setenv("MOOX_FACTOR_DB_PATH", "./env.db")
	t.Setenv("MOOX_FACTOR_ENGINE_WORKER_PATH", "./env-worker.py")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", "/env/python")
	t.Setenv("MOOX_FACTOR_ENGINE_FACTORS_DIR", "./env-factors")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "factor")
	t.Setenv("MOOX_GATEWAY_CALLER", "factor")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-secret")

	resolved, err := resolveRunOnceRuntime(cliConfig{
		ConfigPath: configPath,
		DBPath:     "./cli.db",
		FactorsDir: "./cli-factors",
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(runtimeRoot, "cli.db"), resolved.DBPath)
	require.Equal(t, filepath.Join(runtimeRoot, "cli-factors"), resolved.FactorsDir)
	require.Equal(t, filepath.Join(runtimeRoot, "env-worker.py"), resolved.WorkerPath)
	require.Equal(t, "/env/python", resolved.PythonBin)
	require.Equal(t, "ip://127.0.0.2:11003", resolved.GatewayTarget)
	require.Equal(t, "env-node", resolved.GatewayNodeID)
	require.Equal(t, 1, resolved.Workers)
}

func TestRunInitAndImport(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "factor.db")
	factorsDir := filepath.Join(tmp, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte(
		"def compute(df, params):\n    return {'bias': df['close']}\n",
	), 0o644))
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"init", "--db", dbPath}, &out))
	out.Reset()
	require.NoError(t, run(context.Background(), []string{
		"import", "--db", dbPath, "--factors-dir", factorsDir,
		"--file", filepath.Join(factorsDir, "Bias.py"), "--factor-id", "bias",
		"--input-columns", "close", "--outputs", "bias", "--params-json", "{}",
		"--lookback-periods", "20",
	}, &out))
	require.Contains(t, out.String(), `"ok":true`)
}

func TestRunOnceRequiresRange(t *testing.T) {
	err := runOnce(context.Background(), cliConfig{}, &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}

func TestRunOnceServiceAuthSignsPrimaryRequestFromRuntimeSecret(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", " primary-secret ")
	auth := serviceAuth()
	require.Equal(t, "moox-factor", auth.GetAppId())
	require.Equal(t, mooxsecurity.HMACSHA256Hex(" primary-secret ", []byte("moox-factor")), auth.GetAppKey())
}

func TestExecutableFactorGroupsHonorBindingScope(t *testing.T) {
	factors := []domain.FactorDef{{FactorID: "a"}, {FactorID: "b"}}
	bindings := []domain.FactorBinding{
		{FactorID: "a", SpaceID: "crypto", SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeAll, TargetDataset: "custom"},
		{FactorID: "b", SpaceID: "crypto", SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["ETH"]`},
	}
	groups := executableFactorGroups(factors, bindings, cliConfig{
		SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC", Freq: "1m",
	})
	require.Equal(t, map[string][]domain.FactorDef{"custom": {{FactorID: "a"}}}, groups)
}
