package primarystore

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 写入方报告的周期完成：明细是不透明字节，Storage 按字节原样保存、查询和发布，不解析；重报幂等，内容不同报冲突。
func TestDatasetPeriodCompletedMarkerRoundTripPreservesOpaqueDetail(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := New(Options{Store: store, Snapshot: func() metadata.RequestSnapshot { return factorResultSnapshot{} }})
	require.NoError(t, err)
	auth := &pb.AuthInfo{AppId: "factor"}
	marker := &pb.DatasetPeriodCompletedMarker{
		DatasetId: "result", Frequency: "1m", PeriodTime: 1786032000,
		Status: "degraded", UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"ETH"},
		CauseEventId: "source-ready", CompletedAt: timestamppb.New(time.Unix(1786032001, 0)),
		DetailType: "test.Detail.v1", Detail: []byte{0x0a, 0x00, 0xff, 0x7f},
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "quant", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 1}}}},
	}
	err = store.UpsertFields(ctx, "quant", "result", []*pb.RowFieldUpsert{row})
	require.NoError(t, err)
	var eventID string
	for attempt := 0; attempt < 2; attempt++ {
		appended, err := service.ReportDatasetPeriodCompleted(ctx, &pb.ReportDatasetPeriodCompletedReq{AuthInfo: auth, SpaceId: "quant", Marker: marker})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, appended.GetRetInfo().GetCode(), "ret_info=%v", appended.GetRetInfo())
		if attempt == 0 {
			eventID = appended.GetEventId()
		}
		require.Equal(t, eventID, appended.GetEventId())
	}
	got, err := service.GetDatasetPeriodCompleted(ctx, &pb.GetDatasetPeriodCompletedReq{AuthInfo: auth, SpaceId: "quant", DatasetId: "result", CauseEventId: "source-ready", PeriodTime: marker.GetPeriodTime()})
	require.NoError(t, err)
	require.True(t, got.GetFound())
	require.Equal(t, eventID, got.GetEventId())
	require.True(t, proto.Equal(marker, got.GetMarker()), "marker changed on readback: %v", got.GetMarker())
	entries, err := store.ListOutbox(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "幂等重报不再追加 Outbox 条目；行写入不进 Outbox")
	prepared, err := store.PrepareOutboxPublication(ctx, entries[0].ID, time.Now().UTC())
	require.NoError(t, err)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, "quant", "result")
	require.NoError(t, err)
	_, payload, err := events.DecodeDatasetPeriodCompleted(registry, prepared, subject, eventID)
	require.NoError(t, err)
	require.Equal(t, "producer", payload.GetSource())
	require.Equal(t, marker.GetFailedSubjects(), payload.GetFailedSubjects())
	require.Equal(t, marker.GetDetail(), payload.GetDetail(), "明细字节原样进入事件")
	changed := proto.Clone(marker).(*pb.DatasetPeriodCompletedMarker)
	changed.Detail = []byte("changed")
	conflict, err := service.ReportDatasetPeriodCompleted(ctx, &pb.ReportDatasetPeriodCompletedReq{AuthInfo: auth, SpaceId: "quant", Marker: changed})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, conflict.GetRetInfo().GetCode())
}

func TestDatasetPeriodCompletedMarkerRejectsInvalidMarkersWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := New(Options{Store: store, Snapshot: func() metadata.RequestSnapshot { return factorResultSnapshot{} }})
	require.NoError(t, err)
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
			rsp, err := service.ReportDatasetPeriodCompleted(ctx, &pb.ReportDatasetPeriodCompletedReq{AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "quant", Marker: marker})
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			entries, err := store.ListOutbox(ctx, 0, 10)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
