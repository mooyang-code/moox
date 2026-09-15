package merge

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMergeProcessConfigHonorsRuntimeEnv(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	t.Setenv("MOOX_FACTOR_MERGE_DB_PATH", "/tmp/merge-runtime.db")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET", "ip://203.0.113.20:11003")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID", "storage")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_KEY_ID", "merge")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_HMAC_KEY_FILE", "/tmp/gateway-merge.key")
	t.Setenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE", "/tmp/factor-merge-eventbus.yaml")
	t.Setenv("MOOX_EVENTBUS_NATS_URL", "tls://eventbus.example:4222")
	cfg, err := LoadProcessConfig(filepath.Join(filepath.Dir(file), "..", "..", "config", "merge-app.yaml"))
	require.NoError(t, err)
	require.Equal(t, "/tmp/merge-runtime.db", cfg.Database.Path)
	require.Equal(t, "ip://203.0.113.20:11003", cfg.Storage.GatewayTarget)
	require.Equal(t, "storage", cfg.Storage.GatewayNodeID)
	require.Equal(t, "merge", cfg.Storage.KeyID)
	require.Equal(t, "/tmp/gateway-merge.key", cfg.Storage.HMACKeyFile)
	require.Equal(t, "/tmp/factor-merge-eventbus.yaml", cfg.EventBus.CredentialFile)
	require.Equal(t, []string{"tls://eventbus.example:4222"}, cfg.EventBus.URLs)
	require.Equal(t, 10*time.Second, cfg.EventBus.FetchMaxWait)
}

func TestMergeAssemblerLoadsExampleSpotSwapDefinition(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	cfg, err := LoadProcessConfig(filepath.Join(filepath.Dir(file), "..", "..", "config", "merge-app.yaml"))
	require.NoError(t, err)
	require.Equal(t, "factor-merge-1", cfg.MergeID)
	require.Equal(t, "mdataset_binance_kline_1m", cfg.Definitions[0].DatasetID)
	require.Equal(t, "dataset_binance_spot_kline_1m", cfg.Definitions[0].Sources[0].DatasetID)
	require.Equal(t, "dataset_binance_swap_kline_1m", cfg.Definitions[0].Sources[1].DatasetID)
	require.Contains(t, cfg.Definitions[0].FieldMappings[0].TargetField, "__")
	require.Empty(t, cfg.Definitions[0].UniverseSource)
}
