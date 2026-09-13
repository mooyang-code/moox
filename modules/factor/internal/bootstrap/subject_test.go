package bootstrap

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type bootstrapSubjectTasks func(context.Context, []taskrunner.Task) []taskrunner.Result

func (f bootstrapSubjectTasks) RunAll(ctx context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	return f(ctx, tasks)
}

func TestEngineSubjectRuntimePersistsBeforeAckAndDeduplicatesAfterRestart(t *testing.T) {
	server := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nc, err := nats.Connect(server.URL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage, Duplicates: 100 * time.Millisecond})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "runtime.db")
	replica, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replica.Close()) })
	require.NoError(t, replica.ApplySchema(factorschema.AllSQL()))
	cfg := DefaultEngineApplicationConfig()
	cfg.EventBus.URLs = []string{server.URL()}
	cfg.Engine.FactorsDir = t.TempDir()
	cfg.SubjectBatch.Window, cfg.SubjectBatch.MaxBatch = 10*time.Millisecond, 2
	gate := taskrunner.NewOperationGate()
	runs := atomic.Int32{}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	runner := bootstrapSubjectTasks(func(ctx context.Context, tasks []taskrunner.Task) []taskrunner.Result {
		runs.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		out := make([]taskrunner.Result, len(tasks))
		for i, task := range tasks {
			out[i] = taskrunner.Result{Task: task, Err: ctx.Err()}
		}
		return out
	})
	_, err = StartEngineSubject(ctx, cfg, replica, runner, gate)
	require.ErrorContains(t, err, "activated catalog")
	_, err = replica.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 1, Factors: []domain.FactorDef{{FactorID: "f", Name: "f", FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 2, InputColumns: []string{"close"}, Outputs: []string{"value"}, ParamsJSON: "{}"}}, Bindings: []domain.FactorBinding{{BindingID: "b", BindingGeneration: "g", FactorID: "f", SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]", Status: domain.BindingStatusEnabled, ResultDatasetID: "out", ResultViewID: "out-view"}}})
	require.NoError(t, err)
	consumer, err := StartEngineSubject(ctx, cfg, replica, runner, gate)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, consumer.Close()) })
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	payload := &storagepb.ViewSourceSubjectReady{SourceViewId: "prices", SourceDatasetId: "bars", SubjectId: "BTC", Frequency: "1m", PeriodTime: 60, ActiveIndexId: "index", InputContractVersion: "schema:1", SourceEventId: "source", SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1}
	payload.ReadyAt = timestamppb.Now()
	encoded, err := registry.Encode(events.ViewSourceSubjectReady, payload, events.PublishOptions{EventID: "ready", OccurredAt: time.Now(), SpaceID: "space", SubjectID: "prices"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	msg := &nats.Msg{Subject: encoded.Subject, Data: raw, Header: nats.Header{"Content-Type": []string{events.ContentType}}}
	msg.Header.Set(nats.MsgIdHdr, "ready")
	_, err = js.PublishMsg(msg)
	require.NoError(t, err)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("subject execution did not start")
	}
	inspection, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer inspection.Close()
	var count int
	require.NoError(t, inspection.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_factor_subject_receipts").Scan(&count))
	require.Zero(t, count)
	info, err := js.ConsumerInfo("MOOX_STORAGE", eventconsumer.ViewSourceSubjectConsumerName)
	require.NoError(t, err)
	require.Equal(t, 1, info.NumAckPending)
	close(release)
	acked := func(deliveries uint64) bool {
		info, err := js.ConsumerInfo("MOOX_STORAGE", eventconsumer.ViewSourceSubjectConsumerName)
		return err == nil && info.Delivered.Consumer >= deliveries && info.NumAckPending == 0
	}
	require.Eventually(t, func() bool { return acked(1) }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, inspection.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_factor_subject_receipts WHERE c_status = 'complete'").Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, consumer.Close())
	consumer, err = StartEngineSubject(ctx, cfg, replica, runner, gate)
	require.NoError(t, err)
	// Expire the server's publish dedup window to exercise engine replay fencing.
	time.Sleep(150 * time.Millisecond)
	_, err = js.PublishMsg(msg)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return acked(2) }, 5*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, runs.Load())
}
