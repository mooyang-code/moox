package view

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type subjectPublisherFunc func(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)

func (f subjectPublisherFunc) Publish(ctx context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	return f(ctx, event, payload, opts)
}

func TestSubjectReadyAfterCommitRetriesWithStableBatchIdentity(t *testing.T) {
	ctx := context.Background()
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(svc, nil, "prices-index", "")
	schema := svc.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "schema", 1
	svc.schemas["prices-index"] = schema
	svc.schemas["prices-next"] = schema
	svc.indexEngine["prices-next"] = "query-test"
	svc.views[viewRef{spaceID: "space", viewID: "prices_view"}].next = "prices-next"
	items := []eventconsumer.DatasetRowsBatchItem{}
	for _, subject := range []string{"BTC", "ETH"} {
		row := viewFreshnessRow(subject, "1m", "venue:test", "2026-09-08T10:04:00Z")
		payload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: "space", DatasetId: "market_prices", Rows: []*pb.RowFieldUpsert{row}})
		require.NoError(t, err)
		payload.SpaceId, payload.DatasetId = "space", "market_prices"
		payload.SourceNodeId, payload.SourceSequence = "node", uint64(len(items)+1)
		payload.SourceStoreId = "store"
		items = append(items, eventconsumer.DatasetRowsBatchItem{Message: &eventpb.EventMessage{EventId: subject, SpaceId: "space", SubjectId: "market_prices", OccurredAt: timestamppb.Now()}, Payload: payload})
	}
	failure := errors.New("publish failed")
	ids := map[string]string{}
	attempts := 0
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	svc.readyPublisher = subjectPublisherFunc(func(_ context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
		_, err := registry.Encode(event, payload, opts)
		require.NoError(t, err)
		require.Greater(t, engine.writes["prices-index"], 0, "publish must follow committed write")
		require.Equal(t, engine.writes["prices-index"], engine.writes["prices-next"], "publish failure must not bypass the replacement write")
		require.Equal(t, events.ViewSourceSubjectReady.Name(), event.Name())
		ready := payload.(*storagepb.ViewSourceSubjectReady)
		require.Equal(t, "schema:1", ready.InputContractVersion)
		require.Equal(t, "node", ready.SourceNodeId)
		require.Equal(t, map[string]uint64{"BTC": 1, "ETH": 2}[ready.SubjectId], ready.SourceSequence)
		if old := ids[ready.SourceEventId]; old != "" {
			require.Equal(t, old, opts.EventID)
		}
		ids[ready.SourceEventId] = opts.EventID
		attempts++
		if attempts == 1 {
			return nil, failure
		}
		return &jetstream.PublishAck{}, nil
	})
	require.ErrorIs(t, svc.HandleDatasetRowsBatch(ctx, items), failure)
	// Redelivery may use a different batch boundary, but not a different ID.
	for _, item := range items {
		require.NoError(t, svc.HandleDatasetRows(ctx, item.Message, item.Payload))
	}
	require.Len(t, ids, 2)
	require.Equal(t, 3, attempts)
	engine.writeErrs = map[string]error{"prices-index": errors.New("commit failed")}
	require.Error(t, svc.HandleDatasetRows(ctx, items[0].Message, items[0].Payload))
	require.Equal(t, 3, attempts, "failed write must not publish readiness")
}
