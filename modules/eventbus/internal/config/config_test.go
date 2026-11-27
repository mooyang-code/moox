package config

import (
	"os"
	"path/filepath"
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

func TestLoadIgnoresRemovedEnvironmentOverrides(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_HOST", "0.0.0.0")
	t.Setenv("MOOX_EVENTBUS_PORT", "4333")
	t.Setenv("MOOX_EVENTBUS_SERVER_NAME", "override-name")
	t.Setenv("MOOX_EVENTBUS_STORE_DIR", t.TempDir())
	t.Setenv("MOOX_EVENTBUS_MAX_PAYLOAD_BYTES", "1024")
	cfg := loadRepositoryConfig(t)
	assert.Equal(t, "127.0.0.1", cfg.Broker.Host)
	assert.Equal(t, 4222, cfg.Broker.Port)
	assert.Equal(t, "eventbus-dev-1", cfg.Broker.ServerName)
	assert.Equal(t, "./data/eventbus/jetstream", cfg.Broker.StoreDir)
	assert.Equal(t, 8388608, cfg.Broker.MaxPayloadBytes)
}

func TestLoadRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key": "broker:\n  host: 127.0.0.1\nunknown_key: true\n",
		"removed auth username": "broker:\n  auth:\n    enabled: false\n    username: legacy\n",
		"removed auth password": "broker:\n  auth:\n    enabled: false\n    password: legacy\n",
		"multiple yaml docs":    "broker:\n  host: 127.0.0.1\n---\nbroker:\n  port: 4222\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
}

func TestRejectUnsafeAndInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"root store": func(c *Config) { c.Broker.StoreDir = "/" },
		"bad TLS":    func(c *Config) { c.Broker.TLS.Enabled = true; c.Broker.TLS.CertFile = "cert" },
		"auth missing users_file": func(c *Config) {
			c.Broker.Auth.Enabled = true
		},
		"auth missing internal credential": func(c *Config) {
			c.Broker.Auth.Enabled = true
			c.Broker.Auth.UsersFile = "/etc/moox/users.yaml"
		},
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
