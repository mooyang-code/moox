//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSubjectReadyDuckDBRowIsReadableInsidePublish(t *testing.T) {
	ctx := context.Background()
	svc, err := New(filepath.Join(t.TempDir(), "views"), "secret")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, engine := range svc.engines {
			if closer, ok := engine.(interface{ Close() error }); ok {
				require.NoError(t, closer.Close())
			}
		}
	})
	auth := &pb.AuthInfo{AppId: "test", AppKey: datanode.ServiceAuthKey("secret", "test")}
	columns := []*pb.ViewColumn{{OriginId: "market_prices.close", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "prices-a", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "market_prices", DatasetIds: []string{"market_prices"}, ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema", Columns: columns,
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	row := viewFreshnessRow("BTC", "1m", "venue:test", "2026-09-08T10:04:00Z")
	payload, err := eventmapper.ToEventRows(&pb.RowsUpserted{SpaceId: "space", DatasetId: "market_prices", Rows: []*pb.RowFieldUpsert{row}})
	require.NoError(t, err)
	published := false
	payload.SourceNodeId, payload.SourceSequence = "node", 1
	payload.SourceStoreId = "store"
	message := &eventpb.EventMessage{EventId: "btc-row", SpaceId: "space", SubjectId: "market_prices", OccurredAt: timestamppb.Now()}
	// The first-build row is journaled without an active index or publisher.
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	require.NoError(t, svc.AttachActiveView(&pb.View{SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "market_prices", DatasetIds: []string{"market_prices"}, Engine: "duckdb", ActiveIndexId: "prices-a", ActiveViewRevision: 1, ActiveViewSchemaHash: "schema", ActiveColumns: columns, Status: "active"}))
	svc.readyPublisher = subjectPublisherFunc(func(ctx context.Context, _ events.Event, _ proto.Message, _ events.PublishOptions) (*jetstream.PublishAck, error) {
		rows, err := svc.query(ctx, "prices-a", []*pb.RowKey{row.Key}, nil)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, float64(1), rows[0].GetFields()[0].GetValue().GetDoubleValue())
		published = true
		return &jetstream.PublishAck{}, nil
	})
	require.NoError(t, svc.ReplayPendingSubjects(ctx))
	require.True(t, published)
	published = false
	require.NoError(t, svc.HandleDatasetRows(ctx, message, payload))
	require.True(t, published)
}
