package resample

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNoticePublisherMergesNoticesIntoOneEventPerGroup(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	notice := func(dataset, subject, origin, job string, from, to time.Time) store.BackfillNotice {
		return store.BackfillNotice{SpaceID: "crypto", DatasetID: dataset, Frequency: "5m", SubjectID: subject, Start: from, End: to, Origin: origin, JobID: job}
	}
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{
		notice("dataset_a", "BTC", "auto", "job-1", start, start.Add(time.Hour)),
		notice("dataset_a", "ETH", "auto", "job-1", start.Add(30*time.Minute), start.Add(2*time.Hour)),
		notice("dataset_a", "BTC", "manual", "job-2", start, start.Add(time.Hour)),
		notice("dataset_b", "BTC", "resample", "job-3", start, start.Add(time.Hour)),
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	woken := 0
	publisher := &NoticePublisher{State: mgr.Resample(), Registry: registry, Enabled: true, Wake: func() { woken++ }, Now: func() time.Time { return start.Add(3 * time.Hour) }}

	require.NoError(t, publisher.Flush(ctx))
	require.Equal(t, 1, woken, "封存后唤醒中继")
	pending, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3, "三个分组各一条事件：同数据集不同来源/作业不合并")

	byJob := map[string]*storagepb.CollectorKlineBackfilled{}
	for _, event := range pending {
		message, err := registry.UnmarshalMessage(event.Payload)
		require.NoError(t, err)
		value := decodePayload(t, registry, message)
		byJob[value.GetJobId()] = value
		require.Equal(t, value.GetDatasetId(), message.GetSubjectId())
	}
	merged := byJob["job-1"]
	require.Equal(t, []string{"BTC", "ETH"}, merged.GetSubjectIds())
	require.Equal(t, start, merged.GetStart().AsTime())
	require.Equal(t, start.Add(2*time.Hour), merged.GetEnd().AsTime(), "区间取并集")
	require.Equal(t, "auto", merged.GetOrigin())
	require.Equal(t, "resample", byJob["job-3"].GetOrigin())

	left, _ := mgr.Resample().UnsealedNotices(ctx, 10)
	require.Empty(t, left)
}

func decodePayload(t *testing.T, registry *events.Registry, message *eventpb.EventMessage) *storagepb.CollectorKlineBackfilled {
	t.Helper()
	event, err := registry.ValidateMessage(message)
	require.NoError(t, err)
	payload := event.NewPayload()
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	return payload.(*storagepb.CollectorKlineBackfilled)
}

func TestNoticePublisherBatchIDIsDeterministicSoReplayIsIdempotent(t *testing.T) {
	require.Equal(t, batchID([]int64{3, 1, 2}), batchID([]int64{1, 2, 3}))
	require.NotEqual(t, batchID([]int64{1, 2}), batchID([]int64{1, 2, 3}))
}

func TestNoticePublisherWithoutEventBusKeepsNoticesForRetention(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "A", Start: utc("2026-10-21T10:00:00Z"), End: utc("2026-10-21T11:00:00Z"), Origin: "auto"}}))
	publisher := &NoticePublisher{State: mgr.Resample()}
	require.NoError(t, publisher.Flush(ctx))
	left, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, left, 1)
}

// 补采来源的事件带上范围内全部标的已确认“行情源没有”的 K 线（截到事件范围内，统一数据集带 series_tag），
// 包括这批没有补写的标的：因子要看到一期里所有确认缺失的标的才放行。合成来源的事件、范围外的确认缺失不带。
func TestNoticePublisherCarriesSourceAbsencesOfBackfills(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "job-1"},
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "resample", JobID: "job-2"},
	}))
	require.NoError(t, mgr.Backfill().AddAbsences(ctx, []store.SourceAbsence{
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", SeriesTag: "swap", From: start.Add(-10 * time.Minute), To: start.Add(10 * time.Minute), Bars: 4},
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "ETH", From: start, To: start.Add(5 * time.Minute), Bars: 1},
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", From: start.Add(2 * time.Hour), To: start.Add(3 * time.Hour), Bars: 12},
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher := &NoticePublisher{State: mgr.Resample(), Registry: registry, Absences: mgr.Backfill(), Enabled: true, Now: func() time.Time { return start.Add(3 * time.Hour) }}
	require.NoError(t, publisher.Flush(ctx))
	pending, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	for _, event := range pending {
		message, err := registry.UnmarshalMessage(event.Payload)
		require.NoError(t, err)
		value := decodePayload(t, registry, message)
		if value.GetOrigin() == "resample" {
			require.Empty(t, value.GetAbsent())
			continue
		}
		require.Len(t, value.GetAbsent(), 2, "从新到旧")
		other := value.GetAbsent()[1]
		require.Equal(t, "ETH", other.GetSubjectId(), "这批没有补写的标的的确认缺失也带上")
		require.Empty(t, other.GetSeriesTag())
		absent := value.GetAbsent()[0]
		require.Equal(t, "BTC", absent.GetSubjectId())
		require.Equal(t, "swap", absent.GetSeriesTag())
		require.Equal(t, start, absent.GetStart().AsTime(), "截到事件范围内")
		require.Equal(t, start.Add(10*time.Minute), absent.GetEnd().AsTime())
	}
}

// 未成批的通知超过一次合并的上限时，一次 Flush 循环封存到全部成批，不留给下一次。
func TestNoticePublisherFlushSealsEverythingBeyondOneBatch(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	var notices []store.BackfillNotice
	for i := 0; i < 7; i++ {
		from := start.Add(time.Duration(i) * time.Hour)
		notices = append(notices, store.BackfillNotice{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", Start: from, End: from.Add(time.Hour), Origin: "auto", JobID: "job-1"})
	}
	require.NoError(t, mgr.Resample().AddNotices(ctx, notices))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher := &NoticePublisher{State: mgr.Resample(), Registry: registry, Enabled: true, MaxNotices: 3, Now: func() time.Time { return start.Add(24 * time.Hour) }}
	require.NoError(t, publisher.Flush(ctx))
	left, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, left)
	pending, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3, "7 条通知按上限 3 分三批")
}

// 范围内的确认缺失超过上限时，事件只带结束时间最新的那几段。
func TestNoticePublisherKeepsOnlyTheLatestAbsencesBeyondTheLimit(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "job-1"},
	}))
	var absences []store.SourceAbsence
	for i := 0; i < 4; i++ {
		from := start.Add(time.Duration(i) * 10 * time.Minute)
		absences = append(absences, store.SourceAbsence{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "ETH", From: from, To: from.Add(5 * time.Minute), Bars: 1})
	}
	require.NoError(t, mgr.Backfill().AddAbsences(ctx, absences))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher := &NoticePublisher{State: mgr.Resample(), Registry: registry, Absences: mgr.Backfill(), Enabled: true, MaxAbsences: 2, Now: func() time.Time { return start.Add(3 * time.Hour) }}
	require.NoError(t, publisher.Flush(ctx))
	pending, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	message, err := registry.UnmarshalMessage(pending[0].Payload)
	require.NoError(t, err)
	absent := decodePayload(t, registry, message).GetAbsent()
	require.Len(t, absent, 2)
	require.Equal(t, start.Add(30*time.Minute), absent[0].GetStart().AsTime())
	require.Equal(t, start.Add(20*time.Minute), absent[1].GetStart().AsTime())
}
