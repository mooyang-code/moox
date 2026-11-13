package view

import (
	"context"
	"fmt"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func changeRange(subject, freq string, start, end time.Time) *pb.DatasetChangeRange {
	return &pb.DatasetChangeRange{SubjectId: subject, Freq: freq, StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)}
}

// 回读计划：结束时间加一个频率步长（月按日历）；先按 max_age 截掉过期部分；同一标的相接的范围合并；
// 时间窗相同的标的合成一次读取，超过 100 个标的时不带标的。
func TestPlanFollowReads(t *testing.T) {
	svc := maxAgeService(t) // crypto 5m 保留 35 天，1d 永久
	t0 := maxAgeNow.Add(-time.Hour).Truncate(5 * time.Minute)

	t.Run("全市场一期不带标的", func(t *testing.T) {
		view := &pb.View{SpaceId: "crypto", ViewId: "v", DatasetId: "k", Freq: "5m"}
		var ranges []*pb.DatasetChangeRange
		for i := 0; i < 501; i++ {
			ranges = append(ranges, changeRange(fmt.Sprintf("S%03d", i), "5m", t0, t0))
		}
		reads := svc.planFollowReads(view, []*pb.DatasetChange{{Sequence: 1, Ranges: ranges}})
		require.Len(t, reads, 1)
		require.Nil(t, reads[0].subjects, "超过 100 个标的按时间索引读")
		require.Equal(t, t0, reads[0].start)
		require.Equal(t, t0.Add(5*time.Minute), reads[0].end, "开始等于结束的一期也要读到那一根")
	})

	t.Run("少量标的按 selector 读并合并相接范围", func(t *testing.T) {
		view := &pb.View{SpaceId: "crypto", ViewId: "v", DatasetId: "k", Freq: "5m"}
		reads := svc.planFollowReads(view, []*pb.DatasetChange{
			{Sequence: 1, Ranges: []*pb.DatasetChangeRange{changeRange("A", "5m", t0, t0.Add(5*time.Minute)), changeRange("B", "5m", t0, t0.Add(5*time.Minute)), changeRange("A", "1m", t0, t0)}},
			{Sequence: 2, Ranges: []*pb.DatasetChangeRange{changeRange("A", "5m", t0.Add(10*time.Minute), t0.Add(15*time.Minute))}},
		})
		require.Len(t, reads, 2, "频率不同的范围不读")
		require.Equal(t, []string{"B"}, reads[0].subjects)
		require.Equal(t, t0.Add(10*time.Minute), reads[0].end)
		require.Equal(t, []string{"A"}, reads[1].subjects, "A 的两段相接，合成一段")
		require.Equal(t, t0, reads[1].start)
		require.Equal(t, t0.Add(20*time.Minute), reads[1].end)
	})

	t.Run("max_age 截断过期部分", func(t *testing.T) {
		view := &pb.View{SpaceId: "crypto", ViewId: "v", DatasetId: "k", Freq: "5m"}
		cutoff := maxAgeNow.Add(-35 * 24 * time.Hour)
		reads := svc.planFollowReads(view, []*pb.DatasetChange{{Sequence: 1, Ranges: []*pb.DatasetChangeRange{
			changeRange("OLD", "5m", cutoff.Add(-48*time.Hour), cutoff.Add(-24*time.Hour)),
			changeRange("SPAN", "5m", cutoff.Add(-24*time.Hour), cutoff.Add(time.Hour)),
		}}})
		require.Len(t, reads, 1, "整段过期的范围不读")
		require.Equal(t, []string{"SPAN"}, reads[0].subjects)
		require.Equal(t, cutoff, reads[0].start)
	})

	t.Run("日线与月线的结束时间", func(t *testing.T) {
		day := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)
		reads := svc.planFollowReads(&pb.View{SpaceId: "crypto", ViewId: "v", DatasetId: "k", Freq: "1d"}, []*pb.DatasetChange{{Sequence: 1, Ranges: []*pb.DatasetChangeRange{changeRange("A", "1d", day, day)}}})
		require.Len(t, reads, 1)
		require.Equal(t, day.Add(24*time.Hour), reads[0].end)
		month := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		reads = svc.planFollowReads(&pb.View{SpaceId: "crypto", ViewId: "v", DatasetId: "k", Freq: "1mo"}, []*pb.DatasetChange{{Sequence: 1, Ranges: []*pb.DatasetChangeRange{changeRange("A", "1mo", month, month)}}})
		require.Len(t, reads, 1)
		require.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), reads[0].end, "月线按日历加一个月")
	})
}

// readyPublisherFake 记下每次发布的就绪事件。
type readyPublisherFake struct {
	payloads []*storageeventpb.ViewDataReady
	opts     []events.PublishOptions
}

func (p *readyPublisherFake) Publish(_ context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	p.payloads = append(p.payloads, proto.Clone(payload).(*storageeventpb.ViewDataReady))
	p.opts = append(p.opts, opts)
	return &jetstream.PublishAck{Stream: event.Stream(), Sequence: uint64(len(p.payloads))}, nil
}

// 跟随路径由完成标记组装的就绪事件：账本判定与写入方上报两种来源的各字段与事件 ID。
func TestFollowReadyEventFromMarker(t *testing.T) {
	occurredAt := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	cases := []struct {
		name    string
		view    *pb.View
		payload *storageeventpb.DatasetPeriodCompleted
		want    *storageeventpb.ViewDataReady
		eventID string
	}{
		{
			name: "账本判定",
			view: &pb.View{SpaceId: "quant", ViewId: "kline-view", DatasetId: "kline", Freq: "1m", ActiveIndexId: "kline-a", ActiveViewSchemaHash: "hash-k"},
			payload: &storageeventpb.DatasetPeriodCompleted{
				DatasetId: "kline", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded", Source: "ledger", SnapshotRef: "universe:kline:1m",
				UniverseSubjectIds: []string{"ETH-USDT", "BTC-USDT", "BTC-USDT"}, FailedSubjects: []string{"ETH-USDT"}, CompletedAt: timestamppb.New(occurredAt),
			},
			want: &storageeventpb.ViewDataReady{
				ViewId: "kline-view", ViewConfigId: "hash-k", CompletionEventId: "marker-ledger", DatasetId: "kline", Status: "degraded",
				VisibleScope: "view:kline-view", Frequency: "1m", PeriodTime: 1786032000, FailedScopeRef: "ETH-USDT", ReadyAt: timestamppb.New(occurredAt),
				UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"},
			},
			eventID: stableViewEventID("data-ready", "quant", "kline-view", "1m", "1786032000"),
		},
		{
			name: "写入方上报",
			view: &pb.View{SpaceId: "quant", ViewId: "result-view", DatasetId: "factor-results", ActiveIndexId: "result-a", ViewRevision: 3},
			payload: &storageeventpb.DatasetPeriodCompleted{
				DatasetId: "factor-results", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Source: "producer",
				UniverseSubjectIds: []string{"ETH-USDT", "BTC-USDT"}, CompletedAt: timestamppb.New(occurredAt), CauseEventId: "cause-1",
				DetailType: "test.Detail.v1", Detail: []byte("detail"),
			},
			want: &storageeventpb.ViewDataReady{
				ViewId: "result-view", ViewConfigId: "result-view@3", CompletionEventId: "marker-producer", DatasetId: "factor-results", Status: "complete",
				VisibleScope: "view:result-view", Frequency: "1m", PeriodTime: 1786032000, ReadyAt: timestamppb.New(occurredAt),
				UniverseSubjectIds: []string{"ETH-USDT", "BTC-USDT"}, DetailType: "test.Detail.v1", Detail: []byte("detail"),
			},
			eventID: stableViewEventID("data-ready", "marker-producer", "result-view"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := proto.Marshal(tc.payload)
			require.NoError(t, err)
			marker, err := proto.Marshal(&eventpb.EventMessage{EventId: "marker-" + tc.payload.GetSource(), SpaceId: "quant", OccurredAt: timestamppb.New(occurredAt), Payload: raw})
			require.NoError(t, err)
			publisher := &readyPublisherFake{}
			ref := viewRef{spaceID: "quant", viewID: tc.view.GetViewId()}
			svc := &Service{
				views:          map[viewRef]*viewRuntime{ref: newViewRuntime()},
				catalogViews:   map[viewRef]*pb.View{ref: tc.view},
				readyPublisher: publisher,
			}
			require.NoError(t, svc.publishFollowMarker(context.Background(), ref, &pb.DatasetChange{Sequence: 9, Marker: marker}))
			require.Len(t, publisher.payloads, 1)
			require.True(t, proto.Equal(tc.want, publisher.payloads[0]), "想要=%v\n实际=%v", tc.want, publisher.payloads[0])
			require.Equal(t, events.PublishOptions{EventID: tc.eventID, OccurredAt: occurredAt, SpaceID: "quant", SubjectID: tc.view.GetViewId()}, publisher.opts[0])
		})
	}
}
