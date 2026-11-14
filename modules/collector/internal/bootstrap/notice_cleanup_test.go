package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

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
