package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadRepositoryConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("../../config/app.yaml")
	require.NoError(t, err)
	return cfg
}

func TestDefaultProvidesOnlyProcessDefaults(t *testing.T) {
	cfg := Default()
	assert.Equal(t, 8*1024*1024, cfg.Broker.MaxPayloadBytes)
	assert.Equal(t, 10*time.Second, cfg.Broker.StartupTimeout)
	assert.Empty(t, cfg.Streams)
	assert.Empty(t, cfg.KV)
}

func TestRepositoryConfigDeclaresInfrastructureOnly(t *testing.T) {
	cfg := loadRepositoryConfig(t)
	require.Len(t, cfg.Streams, 5)
	require.Len(t, cfg.KV, 1)
	want := map[string]string{
		"MOOX_CLOUDNODE_EXEC": "work_queue",
		"MOOX_OBSERVABILITY":  "limits",
		"MOOX_STORAGE":        "limits",
		"MOOX_MARKET_FETCH":   "limits",
		"MOOX_TRADE":          "work_queue",
	}
	for _, stream := range cfg.Streams {
		retention, ok := want[stream.Name]
		if !ok {
			t.Fatalf("unexpected stream %q", stream.Name)
		}
		assert.Equal(t, retention, stream.Retention, stream.Name)
		if stream.Name == "MOOX_TRADE" {
			assert.Equal(t, []string{"moox.event.trade.target.weight_requested.v1.>"}, stream.Subjects)
		}
		if stream.Name == "MOOX_STORAGE" {
			assert.Equal(t, []string{
				"moox.event.storage.dataset.rows.upserted.v2.>",
				"moox.event.storage.dataset.period.collected.v1.>",
				"moox.event.storage.view.source_period.ready.v1.>",
				"moox.event.storage.dataset.factor_period.computed.v1.>",
				"moox.event.storage.view.factor_period.ready.v1.>",
				"moox.event.storage.dataset.sync_point.v1.>",
			}, stream.Subjects)
		}
		delete(want, stream.Name)
	}
	assert.Empty(t, want, "missing streams")
	for _, stream := range cfg.Streams {
		assert.NotEqual(t, "MOOX_METRICS", stream.Name)
		if stream.Name == "MOOX_OBSERVABILITY" {
			assert.Equal(t, []string{"moox.event.observability.>"}, stream.Subjects)
		}
	}
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_PORT", "4333")
	t.Setenv("MOOX_EVENTBUS_STORE_DIR", t.TempDir())
	t.Setenv("MOOX_EVENTBUS_STREAM_MAX_BYTES", "104857600")
	cfg := loadRepositoryConfig(t)
	assert.Equal(t, 4333, cfg.Broker.Port)
	for _, stream := range cfg.Streams {
		assert.Equal(t, int64(104857600), stream.MaxBytes)
	}
}

func TestRejectMissingGovernedEventFamily(t *testing.T) {
	cfg := loadRepositoryConfig(t)
	for i := range cfg.Streams {
		if cfg.Streams[i].Name == "MOOX_TRADE" {
			cfg.Streams[i].Subjects = []string{"moox.event.trade.other.>"}
		}
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "governed event")
}

func TestRejectUnsafeAndInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"root store":           func(c *Config) { c.Broker.StoreDir = "/" },
		"duplicate stream":     func(c *Config) { c.Streams = append(c.Streams, c.Streams[0]) },
		"bad TLS":              func(c *Config) { c.Broker.TLS.Enabled = true; c.Broker.TLS.CertFile = "cert" },
		"bad auth":             func(c *Config) { c.Broker.Auth.Enabled = true },
		"cluster default name": func(c *Config) { c.Broker.Cluster.Enabled = true },
		"overlap": func(c *Config) {
			c.Streams[0].Subjects = []string{"moox.event.observability.>"}
		},
		"negative duplicates": func(c *Config) { c.Streams[0].Duplicates = -time.Second },
		"replicas":            func(c *Config) { c.Streams[0].Replicas = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := loadRepositoryConfig(t)
			mutate(cfg)
			require.Error(t, cfg.Validate())
		})
	}
}

func TestSubjectValidation(t *testing.T) {
	require.NoError(t, validateSubject("moox.event.storage.>", true))
	require.Error(t, validateSubject("", true))
	require.Error(t, validateSubject("moox..storage", true))
	require.Error(t, validateSubject("moox.>", false))
	require.Error(t, validateSubject("moox.*.rows", false))

}

func TestPatternOverlap(t *testing.T) {
	assert.True(t, patternsOverlap("moox.>", "moox.storage"))
	assert.True(t, patternsOverlap("moox.event.*.rows", "moox.event.storage.rows"))
	assert.False(t, patternsOverlap("moox.storage", "moox.factor"))
}

func TestUnsafeStoreDir(t *testing.T) {
	assert.True(t, unsafeStoreDir(""))
	assert.True(t, unsafeStoreDir("."))
	assert.True(t, unsafeStoreDir("/"))
	assert.False(t, unsafeStoreDir("./data/eventbus"))
}
