package bootstrap

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/health"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"os"
	"path/filepath"
	"strings"
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

func TestAppRunConsumesStorageEventAndBecomesReadyE2E(t *testing.T) {
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
	const storageSubject = "moox.storage.dataset.rows.upserted.v1.>"
	_, err = js.AddStream(&nats.StreamConfig{Name: events.DatasetRowsUpserted.Stream(), Subjects: []string{storageSubject}, Storage: nats.FileStorage})
	require.NoError(t, err)
	_, err = js.AddConsumer(events.DatasetRowsUpserted.Stream(), &nats.ConsumerConfig{
		Name: cfg.Archive.EventBus.Consumer, Durable: cfg.Archive.EventBus.Consumer,
		FilterSubject: storageSubject, AckPolicy: nats.AckExplicitPolicy,
		AckWait: 5 * time.Minute, MaxDeliver: -1, MaxAckPending: 256,
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
				publishArchiveStorageEvent(t, ctx, ns.ClientURL())
				require.Eventually(t, func() bool {
					info, infoErr := js.ConsumerInfo(events.DatasetRowsUpserted.Stream(), cfg.Archive.EventBus.Consumer)
					return infoErr == nil && info.AckFloor.Consumer >= 1
				}, 5*time.Second, 20*time.Millisecond, "archive did not durably handle and ACK the real storage event")
				cancel()
				select {
				case err := <-errCh:
					// This ingress E2E intentionally has no Storage metadata RPC
					// fixture. The delivery is already journaled and ACKed above;
					// only the separate shutdown materialization step may fail.
					if err != nil && !strings.Contains(err.Error(), "gateway target node is invalid") {
						t.Fatalf("app.Run shutdown: %v", err)
					}
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

func publishArchiveStorageEvent(t *testing.T, ctx context.Context, natsURL string) {
	t.Helper()
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{natsURL}, "archive-app-e2e-publisher"))
	require.NoError(t, err)
	defer client.Close()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	payload := &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto_binance", DatasetId: "spot_kline",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{
				SpaceId: "crypto_binance", DatasetId: "spot_kline",
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-25T00:00:00Z",
				}},
			},
			Fields: []*storagepb.FieldValue{{
				FieldId: "close",
				Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100}},
			}},
		}},
	}
	_, err = publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{
		EventID: "archive-app-e2e-1", OccurredAt: time.Now().UTC(),
		SpaceID: "crypto_binance", SubjectID: "spot_kline",
	})
	require.NoError(t, err)
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
	cfg.Archive.EventBus.Consumer = fmt.Sprintf("archive_test_%d", time.Now().UnixNano())
	cfg.Archive.Materialize.ShutdownTimeout = 5 * time.Second
	cfg.Archive.COS.Enabled = false
	require.NoError(t, cfg.Validate())
	return cfg
}
