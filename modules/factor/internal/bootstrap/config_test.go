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
	require.Equal(t, "factor-mgr", cfg.GatewayClient.Caller)
	require.Empty(t, cfg.GatewayClient.KeyID)
	require.Equal(t, "../../secrets/caller-factor-mgr.key", cfg.GatewayClient.KeyFile)
	require.Equal(t, "python3", cfg.Python.Bin)
	require.Equal(t, 45*time.Second, cfg.Engine.LeaseTTL)
	require.Equal(t, 15*time.Minute, cfg.Engine.JobLeaseTTL)
}

func TestLoadConfigRejectsShortEngineLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("engine:\n  lease_ttl: 2s\n"), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "engine.lease_ttl")
}

func TestLoadRejectsEngineOnlySections(t *testing.T) {
	for _, section := range []string{
		"eventbus:\n  urls: [nats://127.0.0.1:4222]\n",
		"pipeline:\n  read_workers: 4\n",
		"python:\n  workers: 8\n",
		"recalc:\n  chunk_periods: 500\n",
	} {
		path := filepath.Join(t.TempDir(), "app.yaml")
		require.NoError(t, os.WriteFile(path, []byte(section), 0o600))
		_, err := Load(path)
		require.Error(t, err, section)
	}
}

func TestSourceCheckerLoadsModuleAndValidatesComputeContract(t *testing.T) {
	checker := sourceChecker{python: PythonConfig{Bin: "python3"}}
	path := filepath.Join(t.TempDir(), "factor.py")
	require.NoError(t, os.WriteFile(path, []byte("def compute(df, params, context):\n    return df\n"), 0o600))
	require.NoError(t, checker.CheckSource(t.Context(), domain.FactorDef{}, path))
	for _, source := range []string{
		"def other():\n    return 1\n",
		"def compute(:\n",
		"import moox_missing_factor_dependency_for_test\ndef compute(df, params, context):\n    return df\n",
		"raise RuntimeError('top-level load failure')\ndef compute(df, params, context):\n    return df\n",
		"def compute():\n    return 1\n",
	} {
		require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
		require.Error(t, checker.CheckSource(t.Context(), domain.FactorDef{}, path), source)
	}
}

func TestFactorConfigRejectsOldOrInvalidGatewayIdentity(t *testing.T) {
	for _, raw := range []string{
		"storage:\n  gateway_target: ip://127.0.0.1:11003\n",
		"gateway_client:\n  target: ip://127.0.0.1:11003\n",
		"gateway_client:\n  caller: factor\n",
		"gateway_client:\n  key_id: first\n  key_id: second\n",
		"gateway_client:\n  caller: factor-mgr\n---\npython:\n  bin: ignored\n",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.yaml")
			require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
	_, err := Default().OpenGateway(nil)
	require.ErrorContains(t, err, "loaded module configuration")
}
