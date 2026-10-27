package datanode

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 写入方报告的周期完成：明细是不透明字节，Storage 按字节原样保存、查询和发布，不解析。
func TestDatasetPeriodCompletedMarkerRoundTripPreservesOpaqueDetail(t *testing.T) {
	ctx := context.Background()
	secret := "period-marker-test-secret"
	options := Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "db")}}
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey(secret, "primary")}
	marker := &pb.DatasetPeriodCompletedMarker{
		DatasetId: "result", Frequency: "1m", PeriodTime: 1786032000,
		Status: "degraded", UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"ETH"},
		CauseEventId: "source-ready", CompletedAt: timestamppb.New(time.Unix(1786032001, 0)),
		DetailType: "test.Detail.v1", Detail: []byte{0x0a, 0x00, 0xff, 0x7f},
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
				_, err := node.Store().UpsertFields(ctx, "quant", "result", "", []*pb.RowFieldUpsert{row})
				require.NoError(t, err)
			}
			appended, err := node.AppendDatasetPeriodCompleted(ctx, &pb.AppendDatasetPeriodCompletedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: marker})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, appended.GetRetInfo().GetCode())
			if attempt == 0 {
				eventID = appended.GetEventId()
			}
			require.Equal(t, eventID, appended.GetEventId())
			got, err := node.GetDatasetPeriodCompletedMarker(ctx, &pb.GetDatasetPeriodCompletedMarkerReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, DatasetId: "result", CauseEventId: "source-ready", PeriodTime: marker.GetPeriodTime()})
			require.NoError(t, err)
			require.True(t, got.GetFound())
			require.Equal(t, eventID, got.GetEventId())
			require.True(t, proto.Equal(marker, got.GetMarker()), "marker changed on readback: %v", got.GetMarker())
			entries, err := node.Store().ListOutbox(ctx, 0, 10)
			require.NoError(t, err)
			require.Len(t, entries, 2, "idempotent marker retry appended another outbox entry")
			require.Less(t, entries[0].ID, entries[1].ID, "period marker must follow result rows in the shared outbox")
			prepared, err := node.Store().PrepareOutboxPublication(ctx, entries[1].ID, time.Now().UTC())
			require.NoError(t, err)
			retry, err := node.Store().PrepareOutboxPublication(ctx, entries[1].ID, time.Now().UTC())
			require.NoError(t, err)
			require.Equal(t, prepared, retry, "outbox publication retry changed marker bytes")
			registry, err := events.DefaultRegistry()
			require.NoError(t, err)
			subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, "quant", "result")
			require.NoError(t, err)
			_, payload, err := events.DecodeDatasetPeriodCompleted(registry, prepared, subject, eventID)
			require.NoError(t, err)
			require.Equal(t, "producer", payload.GetSource())
			require.Equal(t, marker.GetFailedSubjects(), payload.GetFailedSubjects())
			require.Equal(t, marker.GetCauseEventId(), payload.GetCauseEventId())
			require.Equal(t, marker.GetDetailType(), payload.GetDetailType())
			require.Equal(t, marker.GetDetail(), payload.GetDetail(), "明细字节原样进入事件")
			changed := proto.Clone(marker).(*pb.DatasetPeriodCompletedMarker)
			changed.Detail = []byte("changed")
			conflict, err := node.AppendDatasetPeriodCompleted(ctx, &pb.AppendDatasetPeriodCompletedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: changed})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_CONFLICT, conflict.GetRetInfo().GetCode())
		}()
	}
}

func TestDatasetPeriodCompletedMarkerRejectsInvalidMarkersWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	secret := "period-marker-validation-secret"
	node, err := NewService(Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "db")}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey(secret, "primary")}
	base := &pb.DatasetPeriodCompletedMarker{
		DatasetId: "result", Frequency: "1m", PeriodTime: 1786032000,
		Status: "complete", UniverseSubjectIds: []string{"ETH"},
		CauseEventId: "source-ready", CompletedAt: timestamppb.New(time.Unix(1786032001, 0)),
		DetailType: "test.Detail.v1", Detail: []byte("opaque"),
	}
	for _, tc := range []struct {
		name   string
		mutate func(*pb.DatasetPeriodCompletedMarker)
	}{
		{name: "missing cause_event_id", mutate: func(marker *pb.DatasetPeriodCompletedMarker) { marker.CauseEventId = "" }},
		{name: "detail without a type", mutate: func(marker *pb.DatasetPeriodCompletedMarker) { marker.DetailType = "" }},
		{name: "failed subject outside the universe", mutate: func(marker *pb.DatasetPeriodCompletedMarker) {
			marker.Status = "degraded"
			marker.FailedSubjects = []string{"BTC"}
		}},
		{name: "complete with failed subjects", mutate: func(marker *pb.DatasetPeriodCompletedMarker) { marker.FailedSubjects = []string{"ETH"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := proto.Clone(base).(*pb.DatasetPeriodCompletedMarker)
			tc.mutate(marker)
			rsp, err := node.AppendDatasetPeriodCompleted(ctx, &pb.AppendDatasetPeriodCompletedReq{NodeId: "node-a", SpaceId: "quant", AuthInfo: auth, Marker: marker})
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			entries, err := node.Store().ListOutbox(ctx, 0, 10)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
