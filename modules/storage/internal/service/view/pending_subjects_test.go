package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStartEventConsumerDrainsPendingSubjectsWithoutMaintenance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	go server.Start()
	defer server.Shutdown()
	require.True(t, server.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(server.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: events.StorageViewConsumerStream, Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "pending-startup-test"))
	require.NoError(t, err)
	defer client.Close()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readySubject, err := registry.RenderSubject(events.ViewSourceSubjectReady, "space", "prices_view")
	require.NoError(t, err)
	sub, err := nc.SubscribeSync(readySubject)
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	engine.rows = []*pb.RowFieldValues{{Key: row.Key, Fields: row.Fields}}
	svc, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(svc, nil, "prices-index", "")
	schema := svc.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "schema", 1
	svc.schemas["prices-index"] = schema
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	message := &eventpb.EventMessage{EventId: "restart-source", SpaceId: "space", SubjectId: "market_prices"}
	require.NoError(t, svc.persistPendingSubjects(ctx, viewRef{"space", "prices_view"}, "prices-index", "market_prices", []*pb.RowFieldUpsert{row}, map[*pb.RowFieldUpsert]rowOrigin{row: {message: message, nodeID: "node", storeID: "store", sequence: 1}}))
	filter, err := registry.RenderSubject(events.DatasetRowsUpserted, "space", "market_prices")
	require.NoError(t, err)
	stop, err := svc.StartEventConsumer(ctx, client, EventConsumerOptions{Consumer: events.StorageViewKlineConsumer, FilterSubjects: []string{filter}, FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1})
	require.NoError(t, err)
	defer stop()
	_, err = sub.NextMsg(3 * time.Second)
	require.NoError(t, err, "startup must publish without any maintenance tick")
	entries, err := os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestPendingSubjectSurvivesRestartAndPublishFailure(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, false)
	configureDatasetFreshnessView(svc, nil, "prices-next", "prices-next")
	building := svc.schemas["prices-next"]
	building.SchemaHash, building.ViewVersion = "schema", 1
	svc.schemas["prices-next"] = building
	svc.pendingSubjectsDir = filepath.Join(t.TempDir(), "pending")
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	payload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: "space", DatasetId: "market_prices", Rows: []*pb.RowFieldUpsert{row}})
	require.NoError(t, err)
	payload.SourceNodeId, payload.SourceStoreId, payload.SourceSequence = "node", "store", 1
	message := &eventpb.EventMessage{EventId: "source", SpaceId: "space", SubjectId: "market_prices", OccurredAt: timestamppb.Now()}
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	require.Equal(t, 1, engine.writes["prices-next"])
	entries, err := os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, svc.ReplayPendingSubjects(ctx))

	// A fresh service discovers the same journal after the index is activated.
	restarted, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(restarted, nil, "prices-index", "")
	restarted.pendingSubjectsDir = svc.pendingSubjectsDir
	schema := restarted.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "schema", 1
	restarted.schemas["prices-index"] = schema
	attempts := 0
	failure := errors.New("NATS unavailable")
	restarted.readyPublisher = subjectPublisherFunc(func(_ context.Context, _ events.Event, _ proto.Message, _ events.PublishOptions) (*jetstream.PublishAck, error) {
		attempts++
		if attempts == 1 {
			return nil, failure
		}
		return &jetstream.PublishAck{}, nil
	})
	require.NoError(t, restarted.ReplayPendingSubjects(ctx))
	require.Zero(t, attempts, "a missing active row must not be declared readable")
	engine.rows = []*pb.RowFieldValues{{Key: row.Key, Fields: row.Fields}}
	require.ErrorIs(t, restarted.ReplayPendingSubjects(ctx), failure)
	entries, err = os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, restarted.ReplayPendingSubjects(ctx))
	require.Equal(t, 2, attempts)
	entries, err = os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Equal(t, 1, engine.writes["prices-next"])
	require.Zero(t, engine.writes["prices-index"], "replay must not overwrite newer values with the old event")

	// A queued row must not be reinterpreted under a replacement contract.
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	schema.SchemaHash = "different-source-contract"
	restarted.schemas["prices-index"] = schema
	require.NoError(t, restarted.ReplayPendingSubjects(ctx))
	require.Equal(t, 2, attempts)
	entries, err = os.ReadDir(svc.pendingSubjectsDir)
	require.NoError(t, err)
	require.Empty(t, entries)

	for _, reason := range []string{"primary replacement", "expired retention"} {
		t.Run(reason, func(t *testing.T) {
			require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
			schema.SchemaHash = "schema"
			schema.PrimaryDatasetID = "market_prices"
			metadata := restarted.catalogViews[viewRef{"space", "prices_view"}]
			metadata.KeepDuration = ""
			if reason == "primary replacement" {
				schema.PrimaryDatasetID = "replacement_prices"
			} else {
				metadata.KeepDuration = "1ns"
			}
			restarted.schemas["prices-index"] = schema
			require.NoError(t, restarted.ReplayPendingSubjects(ctx))
			require.Equal(t, 2, attempts, "obsolete rows must not trigger under the current contract")
			entries, err := os.ReadDir(svc.pendingSubjectsDir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}

	// Concurrent activation waits for the preceding drain instead of skipping.
	release, err := restarted.pendingSubjectsGate.lock(ctx)
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	require.ErrorIs(t, restarted.ReplayPendingSubjects(waitCtx), context.DeadlineExceeded)
	release()
}
