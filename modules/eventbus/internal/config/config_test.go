package config

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Broker.MaxPayloadBytes != 8*1024*1024 || c.Broker.StartupTimeout != 10*time.Second {
		t.Fatalf("unexpected broker defaults: %#v", c.Broker)
	}
}

func TestDefaultIncludesArchiveConsumer(t *testing.T) {
	cfg := Default()
	for _, consumer := range cfg.Consumers {
		if consumer.Stream == "MOOX_STORAGE" && consumer.Durable == "moox_archive_kline_v1" {
			if consumer.FilterSubject != "moox.storage.dataset.rows.upserted.v1.>" || consumer.DeliverPolicy != "all" || consumer.AckWait != 5*time.Minute || consumer.MaxDeliver != -1 {
				t.Fatalf("archive consumer = %#v", consumer)
			}
			return
		}
	}
	t.Fatal("archive durable consumer missing")
}

func TestDefaultMonitorConsumersMatchRuntimeContracts(t *testing.T) {
	cfg := Default()
	want := map[string]string{
		"monitor_hostmetrics_ingest_v1": "moox.metrics.host.reported.v1.>",
		"monitor_metrics_ingest_v1":     "moox.metrics.snapshot.reported.v1.>",
	}
	for _, consumer := range cfg.Consumers {
		if topic, ok := want[consumer.Durable]; ok {
			if consumer.FilterSubject != topic || consumer.AckWait != time.Minute || consumer.MaxAckPending != 256 || consumer.MaxDeliver != 3 {
				t.Fatalf("monitor consumer %q = %#v", consumer.Durable, consumer)
			}
			delete(want, consumer.Durable)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing monitor consumers: %#v", want)
	}
}

func TestDefaultTradingSignalConsumerMatchesRuntimeContract(t *testing.T) {
	for _, consumer := range Default().Consumers {
		if consumer.Durable == "trade_trading_signal_v1" {
			if consumer.Stream != "MOOX_TRADE" || consumer.FilterSubject != "moox.trading.signal.v1.>" || consumer.DeliverPolicy != "all" || consumer.AckWait != time.Minute || consumer.MaxAckPending != 256 || consumer.MaxDeliver != -1 {
				t.Fatalf("trading signal consumer = %#v", consumer)
			}
			return
		}
	}
	t.Fatal("trading signal durable consumer missing")
}

func TestRejectMissingGovernedEventFamily(t *testing.T) {
	c := Default()
	for i := range c.Streams {
		if c.Streams[i].Name == "MOOX_TRADE" {
			c.Streams[i].Subjects = []string{"moox.trade.>"}
		}
	}

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "governed event")
}

func TestRejectNarrowGovernedEventFamily(t *testing.T) {
	c := Default()
	for i := range c.Streams {
		if c.Streams[i].Name == "MOOX_MARKET" {
			c.Streams[i].Subjects = []string{"moox.market.tick.received.v1.*", "moox.market.kline.closed.v1.>"}
		}
	}

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "governed event market.tick.received@1")
}

func TestRepositoryConfigLoads(t *testing.T) {
	if _, err := Load("../../config/app.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadYAMLAndEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("broker:\n  store_dir: ./data/test\n  port: 4223\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_EVENTBUS_PORT", "4333")
	t.Setenv("MOOX_EVENTBUS_STREAM_MAX_BYTES", "104857600")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Broker.Port != 4333 || c.Broker.StoreDir != "./data/test" {
		t.Fatalf("overrides not applied: %#v", c.Broker)
	}
	for _, stream := range c.Streams {
		if stream.MaxBytes != 104857600 {
			t.Fatalf("stream %s max bytes = %d", stream.Name, stream.MaxBytes)
		}
	}
}

func TestRejectUnsafeAndInvalidConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"root store":             func(c *Config) { c.Broker.StoreDir = "/" },
		"duplicate stream":       func(c *Config) { c.Streams = append(c.Streams, c.Streams[0]) },
		"bad TLS":                func(c *Config) { c.Broker.TLS.Enabled = true; c.Broker.TLS.CertFile = "cert" },
		"bad auth":               func(c *Config) { c.Broker.Auth.Enabled = true },
		"missing governed event": func(c *Config) { c.Streams[0].Subjects = []string{"moox.other.>"} },
		"cluster default name":   func(c *Config) { c.Broker.Cluster.Enabled = true },
		"overlap":                func(c *Config) { c.Streams[1].Subjects = []string{"moox.metrics.>"} },
		"negative duplicates":    func(c *Config) { c.Streams[0].Duplicates = -time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate returned nil")
			}
		})
	}
}

func TestRejectReplicaWithoutCluster(t *testing.T) {
	c := Default()
	c.Streams[0].Replicas = 2
	if err := c.Validate(); err == nil {
		t.Fatal("replica count was accepted without a cluster")
	}
}

func TestValidateSubject(t *testing.T) {
	require.NoError(t, validateSubject("moox.storage.>", true))
	require.Error(t, validateSubject("", true))
	require.Error(t, validateSubject("moox..storage", true))
	require.Error(t, validateSubject("moox.>", false))
	require.Error(t, validateSubject("moox.*.rows", false))
}

func TestSubjectMatches(t *testing.T) {
	assert.True(t, subjectMatches("moox.storage.>", "moox.storage.rows.v1"))
	assert.True(t, subjectMatches("moox.*.rows", "moox.storage.rows"))
	assert.False(t, subjectMatches("moox.storage.rows", "moox.storage.other"))
}

func TestPatternsOverlap(t *testing.T) {
	assert.True(t, patternsOverlap("moox.>", "moox.storage"))
	assert.True(t, patternsOverlap("moox.*.rows", "moox.storage.rows"))
	assert.False(t, patternsOverlap("moox.storage", "moox.factor"))
}

func TestUnsafeStoreDir(t *testing.T) {
	assert.True(t, unsafeStoreDir(""))
	assert.True(t, unsafeStoreDir("."))
	assert.True(t, unsafeStoreDir("/"))
	assert.False(t, unsafeStoreDir("./data/eventbus"))
}

func TestFindStream(t *testing.T) {
	cfg := Default()
	stream, ok := findStream(cfg, cfg.Streams[0].Name)
	require.True(t, ok)
	assert.Equal(t, cfg.Streams[0].Name, stream.Name)
	_, ok = findStream(cfg, "missing")
	assert.False(t, ok)
}

func TestValidateConsumerTemplate(t *testing.T) {
	cfg := Default()
	template := cfg.ConsumerTemplates[0]
	require.NoError(t, validateConsumerTemplate(&template, cfg))
	bad := template
	bad.Stream = "missing"
	require.Error(t, validateConsumerTemplate(&bad, cfg))
	bad = template
	bad.Stream = "MOOX_TRADE"
	require.Error(t, validateConsumerTemplate(&bad, cfg))
}

func TestDefaultStorageViewConsumerAllowsParallelLanes(t *testing.T) {
	for _, consumer := range Default().Consumers {
		if consumer.Stream == "MOOX_STORAGE" && consumer.Durable == "storage_view" {
			if consumer.MaxAckPending != 8 {
				t.Fatalf("storage_view max_ack_pending = %d, want 8", consumer.MaxAckPending)
			}
			return
		}
	}
	t.Fatal("storage_view durable consumer missing")
}
