package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

// 维护轮的兜底封存只封创建超过一小时的通知：进行中的补采作业刚写下的留给作业结束时封存。
func TestSealStaleNoticesOnlySealsNoticesOlderThanOneHour(t *testing.T) {
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	require.NoError(t, dbm.Resample().AddNotices(ctx, []store.BackfillNotice{
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "job-1"},
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher := &collectorresample.NoticePublisher{State: dbm.Resample(), Registry: registry, Enabled: true}
	now := time.Now().UTC()

	require.NoError(t, sealStaleNotices(ctx, publisher, now.Add(noticeSealAfter-time.Minute)))
	left, err := dbm.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, left, 1, "一小时内的通知不封")

	require.NoError(t, sealStaleNotices(ctx, publisher, now.Add(noticeSealAfter+time.Minute)))
	left, err = dbm.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, left, "超过一小时的通知由维护轮封存")
	pending, err := dbm.Resample().PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

// 补写通知的清理只依赖通知表本身，不需要合成运行时：合成关闭时维护轮照样清掉过期的通知，保留期内的不动。
func TestCleanupNoticeStateWorksWithoutResampleRuntime(t *testing.T) {
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	require.NoError(t, dbm.Resample().AddNotices(ctx, []store.BackfillNotice{
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "job-1"},
	}))

	require.NoError(t, cleanupNoticeState(ctx, dbm.Resample(), time.Now().UTC()))
	left, err := dbm.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, left, 1, "保留期内的通知不清")

	require.NoError(t, cleanupNoticeState(ctx, dbm.Resample(), time.Now().UTC().Add(noticeRetention+time.Hour)))
	left, err = dbm.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, left)
}
