package trigger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

const testMergedDataset = "mdataset_binance_kline_1m"

func TestDatasetRowsTriggerIgnoresFactorPatch(t *testing.T) {
	runner := new(recordingCombinationRunner)
	executor := newDatasetRowsHarness(t, runner, nil)
	payload := readyRowsEvent("BTC-USDT", "ETH-USDT")
	payload.WriteKind = "factor_patch"
	payload.Rows[0].Attributes["moox.binding_version"] = &publicstoragepb.TypedValue{
		Value: &publicstoragepb.TypedValue_StringValue{StringValue: "stale-generation"},
	}
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-patch", payload))
	require.Empty(t, runner.tasks)
}

func TestDatasetRowsTriggerIgnoresUnreadyInput(t *testing.T) {
	runner := new(recordingCombinationRunner)
	executor := newDatasetRowsHarness(t, runner, nil)
	payload := readyRowsEvent("BTC-USDT")
	payload.Rows[0].Attributes["moox.input_ready"] = &publicstoragepb.TypedValue{
		Value: &publicstoragepb.TypedValue_BoolValue{BoolValue: false},
	}
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-unready", payload))
	require.Empty(t, runner.tasks)

	payload.WriteKind = ""
	payload.Rows[0].Attributes["moox.input_ready"] = &publicstoragepb.TypedValue{
		Value: &publicstoragepb.TypedValue_BoolValue{BoolValue: true},
	}
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-generic", payload))
	require.Empty(t, runner.tasks)
}

func TestDatasetRowsTriggerDedupesDuplicateSourceEvent(t *testing.T) {
	runner := new(recordingCombinationRunner)
	db := openDatasetRowsStore(t)
	factorsDir := t.TempDir()
	executor := NewDatasetRowsRunner(db.Bindings(), db.Factors(), runner, db, factorsDir)
	payload := readyRowsEvent("BTC-USDT")
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-1", payload))
	require.Len(t, runner.tasks, 1)
	firstID := runner.tasks[0].TaskID
	require.Equal(t, taskrunner.DeterministicTaskID(runner.tasks[0]), firstID)
	require.Equal(t, "dataset_rows", runner.tasks[0].TriggerType)
	require.Empty(t, runner.tasks[0].ExpectedActiveIndexID)
	require.Zero(t, runner.tasks[0].ExpectedActiveIndexRevision)
	require.Empty(t, runner.tasks[0].InputContractVersion)
	require.Equal(t, testMergedDataset, runner.tasks[0].SourceDataset)
	require.Equal(t, testMergedDataset, runner.tasks[0].ResultDatasetID)

	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-1", payload))
	require.Len(t, runner.tasks, 1)

	restarted := new(recordingCombinationRunner)
	again := NewDatasetRowsRunner(db.Bindings(), db.Factors(), restarted, db, factorsDir)
	require.NoError(t, again.HandleDatasetRows(context.Background(), "event-1", payload))
	require.Empty(t, restarted.tasks)
}

func TestDatasetRowsTriggerDoesNotWaitForUniverse(t *testing.T) {
	runner := new(recordingCombinationRunner)
	executor := newDatasetRowsHarness(t, runner, nil)
	first := readyRowsEvent("BTC-USDT")
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-btc", first))
	require.Len(t, runner.tasks, 1)
	require.Equal(t, "BTC-USDT", runner.tasks[0].SubjectID)

	second := readyRowsEvent("ETH-USDT")
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-eth", second))
	require.Len(t, runner.tasks, 2)
	require.Equal(t, "ETH-USDT", runner.tasks[1].SubjectID)
	require.NotEqual(t, runner.tasks[0].TaskID, runner.tasks[1].TaskID)
}

func TestDatasetRowsTriggerRejectsStaleBindingVersion(t *testing.T) {
	runner := new(recordingCombinationRunner)
	executor := newDatasetRowsHarness(t, runner, nil)
	payload := readyRowsEvent("BTC-USDT")
	payload.Rows[0].Attributes["moox.binding_version"] = &publicstoragepb.TypedValue{
		Value: &publicstoragepb.TypedValue_StringValue{StringValue: "old-binding"},
	}
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-stale", payload))
	require.Empty(t, runner.tasks)
}

func TestDatasetRowsTriggerIgnoresUnboundDataset(t *testing.T) {
	runner := new(recordingCombinationRunner)
	executor := newDatasetRowsHarness(t, runner, nil)
	payload := readyRowsEvent("BTC-USDT")
	payload.DatasetId = "mdataset_other"
	payload.Rows[0].Key.DatasetId = "mdataset_other"
	require.NoError(t, executor.HandleDatasetRows(context.Background(), "event-other", payload))
	require.Empty(t, runner.tasks)
}

func TestDatasetRowsTriggerFirstInstallUsesDeliverNew(t *testing.T) {
	require.Equal(t, nats.DeliverNewPolicy, DatasetRowsDeliverPolicy(nil))
	require.Equal(t, nats.DeliverAllPolicy, DatasetRowsDeliverPolicy(&nats.ConsumerInfo{
		Config: nats.ConsumerConfig{DeliverPolicy: nats.DeliverAllPolicy},
	}))
	require.Equal(t, nats.DeliverNewPolicy, DatasetRowsDeliverPolicy(&nats.ConsumerInfo{
		Config: nats.ConsumerConfig{DeliverPolicy: nats.DeliverNewPolicy},
	}))
}

func TestDatasetRowsTriggerRestartPreservesBacklog(t *testing.T) {
	bus := testkit.Start(t)
	bus.AddStream(t, &nats.StreamConfig{
		Name: events.DatasetRowsUpserted.Stream(), Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage,
	})
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.DatasetRowsUpserted, "crypto", testMergedDataset)
	require.NoError(t, err)

	clientCfg := jetstream.ConfigFromEnv([]string{bus.URL()}, "moox-factor-engine")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := jetstream.Connect(ctx, clientCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	oldRaw, err := registry.MarshalMessage(events.DatasetRowsUpserted, readyRowsEvent("OLD-USDT"), events.PublishOptions{
		EventID: "hist-1", SpaceID: "crypto", SubjectID: testMergedDataset, OccurredAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = client.PublishRaw(ctx, subject, "hist-1", oldRaw, events.ContentType)
	require.NoError(t, err)

	cfg := DatasetRowsConsumerConfig(nil, time.Second, []string{subject})
	require.Equal(t, nats.DeliverNewPolicy, cfg.DeliverPolicy)
	consumer, err := events.NewConsumer(ctx, client, registry, cfg)
	require.NoError(t, err)

	_, fetchErr := consumer.Fetch(ctx, 1)
	require.ErrorIs(t, fetchErr, nats.ErrTimeout)

	liveRaw, err := registry.MarshalMessage(events.DatasetRowsUpserted, readyRowsEvent("BTC-USDT"), events.PublishOptions{
		EventID: "live-1", SpaceID: "crypto", SubjectID: testMergedDataset, OccurredAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = client.PublishRaw(ctx, subject, "live-1", liveRaw, events.ContentType)
	require.NoError(t, err)

	got, err := consumer.Fetch(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, got[0].Nak(ctx, 0))
	require.NoError(t, consumer.Close())

	info, err := bus.JetStream().ConsumerInfo(events.DatasetRowsUpserted.Stream(), DatasetRowsConsumerName)
	require.NoError(t, err)
	require.Equal(t, nats.DeliverNewPolicy, DatasetRowsDeliverPolicy(info))
	require.True(t, info.NumAckPending > 0 || info.NumPending > 0)

	restartCfg := DatasetRowsConsumerConfig(info, 3*time.Second, []string{subject})
	require.Equal(t, nats.DeliverNewPolicy, restartCfg.DeliverPolicy)
	restarted, err := events.NewConsumer(ctx, client, registry, restartCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restarted.Close() })
	var replayed []*jetstream.Delivery
	require.Eventually(t, func() bool {
		batch, fetchErr := restarted.Fetch(ctx, 1)
		if fetchErr != nil {
			return false
		}
		replayed = batch
		return len(batch) == 1
	}, 5*time.Second, 50*time.Millisecond)
	require.Len(t, replayed, 1)
}

func newDatasetRowsHarness(t *testing.T, runner CombinationTaskRunner, db *store.Store) *DatasetRowsRunner {
	t.Helper()
	if db == nil {
		db = openDatasetRowsStore(t)
	}
	return NewDatasetRowsRunner(db.Bindings(), db.Factors(), runner, db, t.TempDir())
}

func openDatasetRowsStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "engine.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	factor := testFactor("bias5")
	factor.SourceCode = "def compute(df, params, context):\n    return df"
	require.NoError(t, db.Factors().Create(context.Background(), factor))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "binding-bias5", FactorID: "bias5", SpaceID: "crypto",
		SourceViewID: testMergedDataset, ResultDatasetID: testMergedDataset, ResultViewID: "view_binance_kline_1m",
		Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
	}))
	return db
}

func readyRowsEvent(subjects ...string) *publicstoragepb.DatasetRowsUpserted {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	rows := make([]*publicstoragepb.RowUpsert, 0, len(subjects))
	for _, subject := range subjects {
		rows = append(rows, &publicstoragepb.RowUpsert{
			Key: &publicstoragepb.RowKey{
				SpaceId: "crypto", DatasetId: testMergedDataset,
				Kind: &publicstoragepb.RowKey_TimeSeries{TimeSeries: &publicstoragepb.TimeSeriesRowKey{
					SubjectId: subject, Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
				}},
			},
			Fields: []*publicstoragepb.FieldValue{{
				FieldId: "dataset_binance_spot_kline_1m__close",
				Value:   &publicstoragepb.TypedValue{Value: &publicstoragepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
			}},
			Attributes: map[string]*publicstoragepb.TypedValue{
				"moox.input_ready": {Value: &publicstoragepb.TypedValue_BoolValue{BoolValue: true}},
				"moox.commit_id":   {Value: &publicstoragepb.TypedValue_StringValue{StringValue: "commit-" + subject}},
			},
		})
	}
	return &publicstoragepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: testMergedDataset, WriteKind: "input_commit",
		SourceNodeId: "node-1", SourceStoreId: "store-1", SourceSequence: 9, Rows: rows,
	}
}
