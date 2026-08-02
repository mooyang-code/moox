package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCommandRequiresCommand(t *testing.T) {
	if _, err := parseArgs(nil); err == nil {
		t.Fatal("parseArgs accepted empty args")
	}
}

func TestParseArgsAcceptsKnownCommands(t *testing.T) {
	cfg, err := parseArgs([]string{"status", "--space", "crypto_market"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "status" || cfg.Space != "crypto_market" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsSeriesTagPresence(t *testing.T) {
	absent, err := parseArgs([]string{"verify"})
	require.NoError(t, err)
	require.Nil(t, absent.SeriesTag)
	empty, err := parseArgs([]string{"verify", "--series-tag", ""})
	require.NoError(t, err)
	require.NotNil(t, empty.SeriesTag)
	assert.Empty(t, *empty.SeriesTag)
	exact, err := parseArgs([]string{"verify", "--series-tag", "venue:binance"})
	require.NoError(t, err)
	require.Equal(t, "venue:binance", *exact.SeriesTag)
}

func TestArchivePrimarySecretUsesFile(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "storage-auth.env")
	require.NoError(t, os.WriteFile(path, []byte(
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n",
	), 0o600))
	secret, err := archivePrimarySecret(path)
	require.NoError(t, err)
	assert.Equal(t, "primary-secret", secret)
}

func TestParseArgsRejectsUnknownCommand(t *testing.T) {
	if _, err := parseArgs([]string{"unknown"}); err == nil {
		t.Fatal("expected unknown command error")
	}
}

func writeTestConfig(t *testing.T) (string, *configPaths) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "archive")
	state := filepath.Join(dir, "state")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(state, 0o755))
	cfgPath := filepath.Join(dir, "app.yaml")
	raw, err := yaml.Marshal(map[string]any{
		"archive": map[string]any{
			"root_dir":  root,
			"state_dir": state,
			"device_id": "test-device",
			"sources": map[string]any{
				"crypto_market": map[string]any{"datasets": []string{"spot_kline_1h"}},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, raw, 0o600))
	return cfgPath, &configPaths{root: root, state: state, path: cfgPath}
}

type configPaths struct {
	root, state, path string
}

func TestRunStatusReportsJournalState(t *testing.T) {
	cfgPath, paths := writeTestConfig(t)
	store, err := journal.Open(paths.state)
	require.NoError(t, err)
	_, err = store.Append(context.Background(), domain.EventBatch{
		MessageID: "m1",
		Rows:      []domain.RowPatch{{Partition: domain.PartitionKey{SpaceID: "crypto_market", DatasetID: "spot_kline_1h", SubjectID: "BTC", Freq: "1h", Month: "202601"}}},
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	var out bytes.Buffer
	err = run(context.Background(), []string{"status", "--config", cfgPath}, &out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	assert.Equal(t, true, payload["ok"])
	assert.Equal(t, float64(1), payload["pending_rows"])
}

func TestRunBackfillRequiresConfirm(t *testing.T) {
	cfgPath, _ := writeTestConfig(t)
	var out bytes.Buffer
	err := run(context.Background(), []string{
		"backfill", "--config", cfgPath,
		"--space", "crypto_market", "--dataset", "spot_kline_1h", "--subject", "BTC-USDT",
		"--freq", "1h", "--series-tag", "", "--start", "2026-01-01T00:00:00Z", "--end", "2026-01-02T00:00:00Z",
	}, &out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	assert.Equal(t, true, payload["confirm_required"])
}

func TestRunCompactMaterializesDirtyRows(t *testing.T) {
	cfgPath, paths := writeTestConfig(t)
	store, err := journal.Open(paths.state)
	require.NoError(t, err)
	closeVal := 1.25
	_, err = store.Append(context.Background(), domain.EventBatch{
		MessageID: "m1",
		Rows: []domain.RowPatch{{
			Partition: domain.PartitionKey{SpaceID: "crypto_market", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202601"},
			DataTime:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			Columns:   map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeVal}},
			WrittenAt: time.Now().UTC(),
		}},
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"compact", "--config", cfgPath}, &out))
	entries, err := os.ReadDir(paths.root)
	require.NoError(t, err)
	assert.NotEmpty(t, entries)
}

func TestRunCompactSeriesTagPresenceSelectsExactIncludingEmpty(t *testing.T) {
	cfgPath, paths := writeTestConfig(t)
	store, err := journal.Open(paths.state)
	require.NoError(t, err)
	closeVal := 1.25
	for i, tag := range []string{"", "venue:binance"} {
		_, err = store.Append(t.Context(), domain.EventBatch{
			MessageID: fmt.Sprintf("m%d", i),
			Rows: []domain.RowPatch{{
				Partition: domain.PartitionKey{SpaceID: "crypto_market", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: tag, Month: "202601"},
				DataTime:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), WrittenAt: time.Now().UTC(),
				Columns: map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeVal}},
			}},
		})
		require.NoError(t, err)
	}
	require.NoError(t, store.Close())

	require.NoError(t, run(t.Context(), []string{"compact", "--config", cfgPath, "--series-tag", ""}, &bytes.Buffer{}))
	store, err = journal.Open(paths.state)
	require.NoError(t, err)
	dirty, err := store.DirtyPartitions(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, dirty, 1)
	assert.Equal(t, "venue:binance", dirty[0].Key.SeriesTag)
	require.NoError(t, store.Close())

	require.NoError(t, run(t.Context(), []string{"compact", "--config", cfgPath}, &bytes.Buffer{}))
	store, err = journal.Open(paths.state)
	require.NoError(t, err)
	dirty, err = store.DirtyPartitions(t.Context(), 10)
	require.NoError(t, err)
	assert.Empty(t, dirty)
	require.NoError(t, store.Close())
}

func TestRunVerifyChecksParquetFiles(t *testing.T) {
	cfgPath, paths := writeTestConfig(t)
	key := domain.PartitionKey{SpaceID: "crypto_market", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202601"}
	abs, err := key.AbsolutePath(paths.root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	closeVal := 1.0
	_, err = parquetio.Write(abs, []domain.ArchiveRow{{
		Partition: key, DataTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		WrittenAt: time.Now().UTC(),
		Columns:   map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeVal}},
	}}, parquetio.WriteOptions{Generation: 1, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"verify", "--config", cfgPath, "--space", "crypto_market", "--series-tag", "venue:binance"}, &out))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["files"])
}

func TestRunSyncCOSDisabled(t *testing.T) {
	cfgPath, _ := writeTestConfig(t)
	err := run(context.Background(), []string{"sync-cos", "--config", cfgPath}, &bytes.Buffer{})
	require.Error(t, err)
}

func TestOpenLocal(t *testing.T) {
	cfgPath, _ := writeTestConfig(t)
	appCfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	store, w, err := openLocal(appCfg)
	require.NoError(t, err)
	require.NotNil(t, store)
	require.NotNil(t, w)
	require.NoError(t, store.Close())
}
