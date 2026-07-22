package bootstrap

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/health"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceLists(t *testing.T) {
	cfg := testConfig()
	got := sourceLists(cfg)
	if len(got["crypto_binance"]) != 2 || got["crypto_binance"][0] != "spot_kline" {
		t.Fatalf("source lists=%v", got)
	}
}

func TestEventBusConfigAppliesCredentialFile(t *testing.T) {
	cfg := config.Default()
	home := t.TempDir()
	credentialFile := filepath.Join(home, ".config", "moox", "eventbus", "archive-eventbus.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(credentialFile), 0o700))
	require.NoError(t, os.WriteFile(credentialFile, []byte("version: 1\nusername: archive-eventbus\ntoken: archive-secret\n"), 0o600))
	t.Setenv("HOME", home)
	cfg.Archive.EventBus.CredentialFile = "~/.config/moox/eventbus/archive-eventbus.yaml"

	got, err := eventBusConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "archive-eventbus", got.Username)
	assert.Equal(t, "archive-secret", got.Password)
}

func testConfig() *config.Config { return config.Default() }

func TestAppRunRequiresConfig(t *testing.T) {
	err := (&App{}).Run(context.Background())
	require.Error(t, err)
}

func TestRegisterHealthRequiresConfig(t *testing.T) {
	err := (&App{}).RegisterHealth(nil)
	require.Error(t, err)
	cfg := testConfig()
	app := &App{Config: cfg, Version: "test", GitCommit: "abc"}
	assert.Nil(t, app.State)
}

func TestAppRunBecomesReadyWithEmbeddedNATS(t *testing.T) {
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(10*time.Second))
	t.Cleanup(ns.Shutdown)

	cfg := archiveTestConfig(t, ns.ClientURL())
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: cfg.Archive.EventBus.Stream, Subjects: []string{cfg.Archive.EventBus.Subject}, Storage: nats.FileStorage})
	require.NoError(t, err)
	_, err = js.AddConsumer(cfg.Archive.EventBus.Stream, &nats.ConsumerConfig{
		Name: cfg.Archive.EventBus.Durable, Durable: cfg.Archive.EventBus.Durable,
		FilterSubject: cfg.Archive.EventBus.Subject, AckPolicy: nats.AckExplicitPolicy,
		AckWait: cfg.Archive.EventBus.AckWait, MaxDeliver: -1, MaxAckPending: cfg.Archive.EventBus.MaxAckPending,
	})
	require.NoError(t, err)

	state := health.New("archive", "archive", "test", "abc")
	app := &App{Config: cfg, State: state}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			t.Fatalf("app.Run exited before ready: %v", err)
		default:
			// Read ReadyFlag via the shared state pointer to avoid racing app.State assignment.
			if state.ReadyFlag.Load() {
				assert.True(t, state.JournalReady.Load())
				assert.True(t, state.NATSReady.Load())
				cancel()
				select {
				case err := <-errCh:
					assert.NoError(t, err)
				case <-time.After(10 * time.Second):
					t.Fatal("app.Run did not stop after cancel")
				}
				return
			}
			select {
			case <-deadline:
				t.Fatal("archive app did not become ready")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func TestRunFromConfigRejectsMissingFile(t *testing.T) {
	err := RunFromConfig(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), "v", "c")
	require.Error(t, err)
}

func archiveTestConfig(t *testing.T, natsURL string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Archive.RootDir = filepath.Join(dir, "archive")
	cfg.Archive.StateDir = filepath.Join(dir, "state")
	keyFile := filepath.Join(dir, "archive.key")
	require.NoError(t, os.WriteFile(keyFile, []byte("archive-test-secret\n"), 0o600))
	cfg.Archive.StorageRPC.KeyID = "archive"
	cfg.Archive.StorageRPC.HMACKeyFile = keyFile
	cfg.Archive.EventBus.URLs = []string{natsURL}
	cfg.Archive.EventBus.Stream = fmt.Sprintf("MOOX_STORAGE_%d", time.Now().UnixNano())
	cfg.Archive.EventBus.Subject = "moox.storage.fields_changed.v1.>"
	cfg.Archive.EventBus.Durable = fmt.Sprintf("archive_test_%d", time.Now().UnixNano())
	cfg.Archive.Materialize.ShutdownTimeout = 5 * time.Second
	cfg.Archive.COS.Enabled = false
	require.NoError(t, cfg.Validate())
	return cfg
}
