package config

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadRepositoryConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("../../config/app.yaml")
	require.NoError(t, err)
	return cfg
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_PORT", "4333")
	t.Setenv("MOOX_EVENTBUS_STORE_DIR", t.TempDir())
	cfg := loadRepositoryConfig(t)
	assert.Equal(t, 4333, cfg.Broker.Port)
}

func TestRejectUnsafeAndInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"root store": func(c *Config) { c.Broker.StoreDir = "/" },
		"bad TLS":    func(c *Config) { c.Broker.TLS.Enabled = true; c.Broker.TLS.CertFile = "cert" },
		"bad auth":   func(c *Config) { c.Broker.Auth.Enabled = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := loadRepositoryConfig(t)
			mutate(cfg)
			require.Error(t, cfg.Validate())
		})
	}
}

func TestUnsafeStoreDir(t *testing.T) {
	assert.True(t, unsafeStoreDir(""))
	assert.True(t, unsafeStoreDir("."))
	assert.True(t, unsafeStoreDir("/"))
	assert.False(t, unsafeStoreDir("./data/eventbus"))
}

func TestDefaultProvidesOnlyProcessDefaults(t *testing.T) {
	cfg := Default()
	assert.Equal(t, 8*1024*1024, cfg.Broker.MaxPayloadBytes)
	assert.Equal(t, 10*time.Second, cfg.Broker.StartupTimeout)
	assert.Equal(t, events.Streams(), cfg.Streams)
}

// Stream 来自拓扑声明：配置文件里不再写 streams。
func TestRepositoryConfigTakesStreamsFromTopology(t *testing.T) {
	cfg := loadRepositoryConfig(t)
	want := map[string]bool{"MOOX_COLLECTOR": false, "MOOX_OBSERVABILITY": false, "MOOX_STORAGE": false, "MOOX_TRADE": true}
	require.Len(t, cfg.Streams, len(want))
	for _, stream := range cfg.Streams {
		workQueue, ok := want[stream.Name]
		require.True(t, ok, stream.Name)
		assert.Equal(t, workQueue, stream.WorkQueue, stream.Name)
		switch stream.Name {
		case "MOOX_TRADE":
			assert.Equal(t, []string{"moox.event.trade.target.weight_requested.v1.>"}, stream.Subjects)
		case "MOOX_COLLECTOR":
			assert.Equal(t, []string{"moox.event.collector.instrument.delist_scheduled.v1.>", "moox.event.collector.kline.backfilled.v1.>"}, stream.Subjects)
		case "MOOX_STORAGE":
			assert.Equal(t, []string{"moox.event.storage.dataset.period.completed.v1.>", "moox.event.storage.view.data.ready.v2.>"}, stream.Subjects)
			assert.Equal(t, int64(2147483648), stream.MaxBytes)
		}
	}
}
