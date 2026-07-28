package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	jsclient "github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestRealtimeEventToPythonWritebackE2E(t *testing.T) {
	root := ".."
	factorsDir := t.TempDir()
	factorPath := filepath.Join(factorsDir, "ExcessReturn.py")
	source := []byte(`def compute(df, params):
    excess = df["nav"] - df["benchmark_return"]
    return {
        "excess_return": excess,
        "rolling_rank": excess.rolling(
            int(params["window"]),
            min_periods=1,
        ).rank(),
    }
`)
	require.NoError(t, os.WriteFile(factorPath, source, 0o600))
	sum := sha256.Sum256(source)
	first := time.Date(2026, 7, 26, 0, 2, 0, 0, time.UTC)
	second := first.Add(time.Nanosecond)

	batcher := trigger.NewEventBatcher(20*time.Millisecond, []domain.FactorBinding{
		{
			BindingID: "bind-excess", FactorID: "excess-return", SpaceID: "quant",
			SourceDataset: "portfolio", Freq: "1m", SubjectMode: domain.SubjectModeAll,
			SubjectsJSON: "[]", TargetDataset: "portfolio_factor", Status: domain.BindingStatusEnabled,
		},
	})
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(10*time.Second))
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	js, err := nc.JetStream()
	require.NoError(t, err)
	family, err := registry.FamilyPattern(events.DatasetRowsUpserted)
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name: events.DatasetRowsUpserted.Stream(), Subjects: []string{family},
		Storage: nats.MemoryStorage,
	})
	require.NoError(t, err)
	nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	consumer := eventconsumer.New(eventconsumer.Config{
		URLs: []string{ns.ClientURL()}, FetchMaxWait: 50 * time.Millisecond,
	}, batcher)
	require.NoError(t, consumer.Start(ctx))
	t.Cleanup(func() { _ = consumer.Close() })
	require.True(t, consumer.Ready())
	client, err := jsclient.Connect(ctx, jsclient.ConfigFromEnv([]string{ns.ClientURL()}, "factor-e2e-publisher"))
	require.NoError(t, err)
	defer client.Close()
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	payload := &storagepb.DatasetRowsUpserted{
		SpaceId: "quant", DatasetId: "portfolio",
		Rows: []*storagepb.RowUpsert{
			eventRow("quant", "portfolio", "fund-a", "1m", first),
			eventRow("quant", "portfolio", "fund-a", "1m", second),
		},
	}
	_, err = publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{
		EventID: "factor-e2e-1", OccurredAt: first, SpaceID: "quant", SubjectID: "portfolio",
	})
	require.NoError(t, err)
	var requests []trigger.Task
	require.Eventually(t, func() bool {
		requests = batcher.Flush(time.Now().Add(time.Second))
		return len(requests) == 1
	}, 5*time.Second, 20*time.Millisecond)
	require.Len(t, requests, 1)
	require.Equal(t, []string{"excess-return"}, requests[0].FactorIDs)
	require.Equal(t, first, requests[0].StartTime)
	require.Equal(t, second.Add(time.Nanosecond), requests[0].EndTime)

	ackConn, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	defer ackConn.Close()
	ackJS, err := ackConn.JetStream()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, infoErr := ackJS.ConsumerInfo(events.DatasetRowsUpserted.Stream(), eventconsumer.DatasetRowsConsumerName)
		return infoErr == nil && info.NumAckPending == 0
	}, 5*time.Second, 20*time.Millisecond)

	task, err := scheduler.BuildTask(scheduler.TaskScope{
		TaskID: "e2e-factor", TriggerType: "event", SpaceID: requests[0].SpaceID,
		SourceDataset: requests[0].SourceDataset, TargetDataset: requests[0].TargetDataset,
		SubjectID: requests[0].SubjectID, Freq: requests[0].Freq,
		StartTime: requests[0].StartTime, EndTime: requests[0].EndTime,
	}, []domain.FactorDef{{
		FactorID: "excess-return", Name: "ExcessReturn", SourceHash: hex.EncodeToString(sum[:]),
		SourcePath: factorPath, InputColumns: []string{"nav", "benchmark_return"},
		Outputs: []string{"excess_return", "rolling_rank"}, ParamsJSON: `{"window":2}`,
		LookbackRows: 2, Status: domain.FactorStatusEnabled,
	}}, factorsDir)
	require.NoError(t, err)

	pythonExec, err := engine.NewPythonExecutor(context.Background(), 1, process.Config{
		PythonBin: "python3", WorkerPath: filepath.Join(root, "pyworker", "worker.py"),
		Args: []string{"--factors-dir", factorsDir}, Limits: process.DefaultLimits(),
	})
	require.NoError(t, err)
	defer pythonExec.Close()
	storage := &storageFake{targets: []time.Time{first, second}}
	service := scheduler.NewService(scheduler.Config{Workers: 1, QueueCapacity: 8}, storage, pythonExec)
	require.NoError(t, service.Enqueue(context.Background(), task))
	require.NoError(t, service.Drain(context.Background()))

	require.Equal(t, 1, storage.writes)
	require.Equal(t, []string{"benchmark_return", "nav"}, storage.requestedColumns)
	for _, forbidden := range []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"} {
		require.False(t, slices.Contains(storage.requestedColumns, forbidden), "implicit OHLCV column %q", forbidden)
	}
	require.ElementsMatch(t, []string{"excess_return", "rolling_rank"}, mapKeys(storage.result.Columns))
	require.Equal(t, []time.Time{first, second}, storage.writtenTimes)
	require.Equal(t, []any{0.04, 0.16}, storage.result.Columns["excess_return"])
	require.Equal(t, []any{1.0, 2.0}, storage.result.Columns["rolling_rank"])
}

func eventRow(spaceID, datasetID, subjectID, freq string, at time.Time) *storagepb.RowUpsert {
	return &storagepb.RowUpsert{Key: &storagepb.RowKey{
		SpaceId: spaceID, DatasetId: datasetID,
		Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: subjectID, Freq: freq, DataTime: at.Format(time.RFC3339Nano),
		}},
	}}
}

type storageFake struct {
	targets          []time.Time
	requestedColumns []string
	writtenTimes     []time.Time
	writes           int
	result           *engine.FactorResult
}

func (s *storageFake) ReadRangeChunk(_ context.Context, _ storageio.WindowKey, start, end time.Time, _ int, _ int, columns []string) (*storageio.RangeChunk, error) {
	s.requestedColumns = append([]string(nil), columns...)
	if !start.Equal(s.targets[0]) || !end.Equal(s.targets[1].Add(time.Nanosecond)) {
		panic("unexpected requested range")
	}
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: columns,
			Rows: [][]any{
				valuesFor(columns, map[string]any{"nav": 0.05, "benchmark_return": 0.01}),
				valuesFor(columns, map[string]any{"nav": 0.18, "benchmark_return": 0.02}),
			},
			DataTimes: append([]time.Time(nil), s.targets...),
		},
		TargetTimes: append([]time.Time(nil), s.targets...),
	}, nil
}

func (s *storageFake) WriteFactorPatch(_ context.Context, _ *engine.FactorTask, times []time.Time, result *engine.FactorResult) error {
	s.writes++
	s.writtenTimes = append([]time.Time(nil), times...)
	s.result = result
	return nil
}

func valuesFor(columns []string, values map[string]any) []any {
	row := make([]any, len(columns))
	for i, column := range columns {
		row[i] = values[column]
	}
	return row
}

func mapKeys(values map[string][]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
