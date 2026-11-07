package resample

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type recordingSync struct {
	mu    sync.Mutex
	calls [][2]string
}

func (r *recordingSync) AppendDatasetSyncPoint(_ context.Context, _, datasetID, requestID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{datasetID, requestID})
	return nil
}

func TestNoticePublisherMergesNoticesIntoOneEventPerGroupWithSyncPoint(t *testing.T) {
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
	sync := &recordingSync{}
	woken := 0
	publisher := &NoticePublisher{State: mgr.Resample(), Sync: sync, Registry: registry, Enabled: true, Wake: func() { woken++ }, Now: func() time.Time { return start.Add(3 * time.Hour) }}

	require.NoError(t, publisher.Flush(ctx))
	require.Equal(t, 1, woken, "封存后唤醒中继")
	pending, err := mgr.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3, "三个分组各一条事件：同数据集不同来源/作业不合并")
	require.Len(t, sync.calls, 3, "每个分组先追加一个同步点")

	byJob := map[string]*storagepb.CollectorKlineBackfilled{}
	for _, event := range pending {
		message, err := registry.UnmarshalMessage(event.Payload)
		require.NoError(t, err)
		value := decodePayload(t, registry, message)
		byJob[value.GetJobId()] = value
		require.Equal(t, value.GetDatasetId(), message.GetSubjectId())
		require.Equal(t, value.GetSyncRequestId(), message.GetEventId(), "同步点 request_id 就是事件 ID")
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
