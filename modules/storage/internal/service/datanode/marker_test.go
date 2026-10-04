package datanode

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFactorPeriodComputedMarkerRoundTripPreservesFactors(t *testing.T) {
	ctx := context.Background()
	secret := "factor-marker-test-secret"
	options := Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "db")}}
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey(secret, "primary")}
	marker := &pb.FactorPeriodComputedMarker{
		DatasetId: "result", SourceDatasetId: "prices", Frequency: "1m", PeriodTime: 1786032000,
		Status: "degraded", UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"ETH"},
		Factors: []*pb.FactorPeriodState{
			{FactorId: "z-factor", Status: "degraded", FailedSubjects: []string{"ETH", "BTC"}, SourceHash: "hash-z"},
			{FactorId: "a-factor", Status: "complete", SourceHash: "hash-a"},
		},
		TriggerEventId: "source-ready", ComputedAt: timestamppb.New(time.Unix(1786032001, 0)),
	}
	var eventID string
	for attempt := 0; attempt < 2; attempt++ {
		node, err := NewService(options)
		require.NoError(t, err)
		func() {
			defer func() { require.NoError(t, node.Close()) }()
			if attempt == 0 {
				row := &pb.RowFieldUpsert{
					Key:    &pb.RowKey{SpaceId: "quant", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row", Version: "1"}}},
					Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 1}}}},
				}
				_, err := node.Store().UpsertFieldsEvent(ctx, []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
					return pebble.BuildDatasetRowsUpsertedMessage("node-a", spaceID, datasetID, rows)
				})
				require.NoError(t, err)
			}
			appended, err := node.AppendFactorPeriodComputed(ctx, &pb.AppendFactorPeriodComputedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: marker})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, appended.GetRetInfo().GetCode())
			if attempt == 0 {
				eventID = appended.GetEventId()
			}
			require.Equal(t, eventID, appended.GetEventId())
			got, err := node.GetFactorPeriodComputedMarker(ctx, &pb.GetFactorPeriodComputedMarkerReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, DatasetId: "result", TriggerEventId: "source-ready", PeriodTime: marker.GetPeriodTime()})
			require.NoError(t, err)
			require.True(t, got.GetFound())
			require.Equal(t, eventID, got.GetEventId())
			require.True(t, proto.Equal(marker, got.GetMarker()), "marker changed on readback: %v", got.GetMarker())
			entries, err := node.Store().ListOutbox(ctx, 0, 10)
			require.NoError(t, err)
			require.Len(t, entries, 2, "idempotent marker retry appended another outbox entry")
			require.Less(t, entries[0].ID, entries[1].ID, "factor marker must follow result rows in the shared outbox")
			prepared, err := node.Store().PrepareOutboxPublication(ctx, entries[1].ID, time.Now().UTC())
			require.NoError(t, err)
			retry, err := node.Store().PrepareOutboxPublication(ctx, entries[1].ID, time.Now().UTC())
			require.NoError(t, err)
			require.Equal(t, prepared, retry, "outbox publication retry changed marker bytes")
			registry, err := events.DefaultRegistry()
			require.NoError(t, err)
			subject, err := registry.RenderSubject(events.FactorPeriodComputed, "quant", "result")
			require.NoError(t, err)
			_, payload, err := events.DecodeFactorPeriodComputed(registry, prepared, subject, eventID)
			require.NoError(t, err)
			require.Equal(t, marker.GetSourceDatasetId(), payload.GetSourceDatasetId())
			require.Equal(t, marker.GetFailedSubjects(), payload.GetFailedSubjects())
			require.Len(t, payload.GetFactors(), len(marker.GetFactors()))
			for i, state := range marker.GetFactors() {
				want := &storageeventpb.FactorPeriodState{FactorId: state.GetFactorId(), Status: state.GetStatus(), FailedSubjects: state.GetFailedSubjects(), SourceHash: state.GetSourceHash()}
				require.True(t, proto.Equal(want, payload.GetFactors()[i]), "factor %d changed in outbox", i)
			}
			changed := proto.Clone(marker).(*pb.FactorPeriodComputedMarker)
			changed.Factors[0].SourceHash = "changed"
			conflict, err := node.AppendFactorPeriodComputed(ctx, &pb.AppendFactorPeriodComputedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: changed})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_CONFLICT, conflict.GetRetInfo().GetCode())
		}()
	}
}

func TestFactorPeriodComputedMarkerRejectsInvalidFactorsWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	secret := "factor-marker-validation-secret"
	node, err := NewService(Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "db")}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey(secret, "primary")}
	base := &pb.FactorPeriodComputedMarker{
		DatasetId: "result", SourceDatasetId: "prices", Frequency: "1m", PeriodTime: 1786032000,
		Status: "complete", UniverseSubjectIds: []string{"ETH"},
		Factors:        []*pb.FactorPeriodState{{FactorId: "factor", Status: "complete", SourceHash: "hash"}},
		TriggerEventId: "source-ready", ComputedAt: timestamppb.New(time.Unix(1786032001, 0)),
	}
	for _, tc := range []struct {
		name   string
		mutate func(*pb.FactorPeriodComputedMarker)
	}{
		{name: "nil factor cannot be dropped", mutate: func(marker *pb.FactorPeriodComputedMarker) { marker.Factors = append(marker.Factors, nil) }},
		{name: "missing source hash", mutate: func(marker *pb.FactorPeriodComputedMarker) { marker.Factors[0].SourceHash = "" }},
		{name: "duplicate factor", mutate: func(marker *pb.FactorPeriodComputedMarker) {
			marker.Factors = append(marker.Factors, proto.Clone(marker.Factors[0]).(*pb.FactorPeriodState))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := proto.Clone(base).(*pb.FactorPeriodComputedMarker)
			tc.mutate(marker)
			rsp, err := node.AppendFactorPeriodComputed(ctx, &pb.AppendFactorPeriodComputedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: marker})
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			entries, err := node.Store().ListOutbox(ctx, 0, 10)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
