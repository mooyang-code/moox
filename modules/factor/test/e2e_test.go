package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
	factorPath := filepath.Join(root, "factors", "Bias.py")
	source, err := os.ReadFile(factorPath)
	require.NoError(t, err)
	sum := sha256.Sum256(source)
	at := time.Date(2026, 7, 26, 0, 2, 0, 0, time.UTC)

	batcher := trigger.NewEventBatcher(20*time.Millisecond, []domain.FactorBinding{{
		BindingID: "bind-bias", FactorID: "bias", SpaceID: "crypto",
		SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeAll,
		SubjectsJSON: "[]", TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}})
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
		SpaceId: "crypto", DatasetId: "bars",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{
			SpaceId: "crypto", DatasetId: "bars",
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
			}},
		}}},
	}
	_, err = publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{
		EventID: "factor-e2e-1", OccurredAt: at, SpaceID: "crypto", SubjectID: "bars",
	})
	require.NoError(t, err)
	var requests []trigger.Task
	require.Eventually(t, func() bool {
		requests = batcher.Flush(time.Now().Add(time.Second))
		return len(requests) == 1
	}, 5*time.Second, 20*time.Millisecond)
	require.Len(t, requests, 1)

	task, err := scheduler.BuildTask(scheduler.TaskScope{
		TaskID: "e2e-factor", TriggerType: "event", SpaceID: requests[0].SpaceID,
		SourceDataset: requests[0].SourceDataset, TargetDataset: requests[0].TargetDataset,
		SubjectID: requests[0].SubjectID, Freq: requests[0].Freq,
		StartTime: requests[0].BarTime, EndTime: requests[0].BarTime.Add(time.Nanosecond),
	}, []domain.FactorDef{{
		FactorID: "bias", Name: "Bias", SourceHash: hex.EncodeToString(sum[:]),
		SourcePath: factorPath, Periods: []int{2}, LookbackBars: 3,
		Status: domain.FactorStatusEnabled,
	}}, filepath.Join(root, "factors"))
	require.NoError(t, err)

	pythonExec, err := engine.NewPythonExecutor(context.Background(), 1, process.Config{
		PythonBin: "python3", WorkerPath: filepath.Join(root, "pyworker", "worker.py"),
		Args:   []string{"--factors-dir", filepath.Join(root, "factors")},
		Limits: process.DefaultLimits(),
	})
	require.NoError(t, err)
	defer pythonExec.Close()
	storage := &storageFake{target: at}
	service := scheduler.NewService(scheduler.Config{Workers: 1, QueueCapacity: 8}, storage, pythonExec)
	require.NoError(t, service.Enqueue(context.Background(), task))
	require.NoError(t, service.Drain(context.Background()))
	require.Equal(t, 1, storage.writes)
	require.Equal(t, []any{101.0 / 100.5}, storage.result.Columns["Bias_2"])
}

type storageFake struct {
	target time.Time
	writes int
	result *engine.FactorResult
}

func (s *storageFake) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	times := []time.Time{s.target.Add(-time.Minute), s.target}
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: []string{"close"}, Rows: [][]any{{100.0}, {101.0}}, DataTimes: times,
		},
		TargetTimes: []time.Time{s.target},
	}, nil
}

func (s *storageFake) WriteFactorPatch(_ context.Context, _ *engine.FactorTask, times []time.Time, result *engine.FactorResult) error {
	s.writes++
	s.result = result
	if len(times) != 1 || !times[0].Equal(s.target) {
		panic("unexpected target range")
	}
	return nil
}
