package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	storagepebble "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/stretchr/testify/require"
)

func TestPeriodInventoryCommandEmitsScopedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := storagepebble.Open(storagepebble.Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	expectation := storagepebble.DatasetPeriodExpectation{
		SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: 1_790_798_400,
		SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: time.Now().Add(time.Hour).Unix(),
		SeriesSnapshot: []storagepebble.DatasetPeriodSeries{{SeriesIndex: 0, SubjectID: "BTC-USDT", SeriesTag: "default"}},
	}
	_, err = store.EnsureDatasetPeriod(context.Background(), expectation)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	var stdout bytes.Buffer
	err = runCommand([]string{
		"period-inventory", "--path", path, "--node-id", "node-a", "--space", "crypto", "--dataset", "bars", "--frequency", "1m",
		"--period-time-min", "1790798400", "--period-time-max", "1790798400",
	}, &stdout, &bytes.Buffer{})
	require.NoError(t, err)
	var got storagepebble.PeriodLedgerInventory
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.Equal(t, 1, got.PeriodCount)
	require.Equal(t, "node-a", got.SourceDataNodeID)
	require.Equal(t, "series_snapshot", got.Periods[0].SnapshotFormat)
	require.Equal(t, "waiting", got.Periods[0].Status)
	require.NotContains(t, stdout.String(), "BTC-USDT", "the report must not export snapshot subjects")
}

func TestPeriodInventoryCommandRequiresExactBoundedScope(t *testing.T) {
	err := runCommand([]string{"period-inventory", "--path", "unused", "--node-id", "node-a", "--dataset", "bars", "--frequency", "1m", "--period-time-min", "1", "--period-time-max", "1"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "space_id")
}

func TestPeriodInventoryCommandFailsWhileDataNodeOwnsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := storagepebble.Open(storagepebble.Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()

	var stdout bytes.Buffer
	err = runCommand([]string{
		"period-inventory", "--path", path, "--node-id", "node-a", "--space", "crypto", "--dataset", "bars", "--frequency", "1m",
		"--period-time-min", "1", "--period-time-max", "1",
	}, &stdout, &bytes.Buffer{})
	require.ErrorContains(t, err, "read-only")
	require.Empty(t, stdout.String())
}
