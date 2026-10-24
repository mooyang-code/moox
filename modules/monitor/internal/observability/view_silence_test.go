package observability

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 视图只在输出提交时刷新水位指标：低频（周期不短于 5 分钟）视图静默不超过一个周期再加 5 分钟不算“生产端停止上报”，
// 超过了才算；分钟级视图沿用通用的 2 分钟判定。
func TestOverviewToleratesQuietLowFrequencyViews(t *testing.T) {
	now := time.Now().UTC()
	view := func(id, freq string) string {
		return fmt.Sprintf(`{"dataset_id":"dataset_%s","freq":"%s","space_id":"crypto","view_id":"view_%s"}`, id, freq, id)
	}
	seedView := func(db *gorm.DB, id, freq string, silent time.Duration, bar time.Duration) {
		watermark := now.Truncate(bar).Add(-bar)
		seen := now.Add(-silent)
		seedOverviewMetricForInstance(t, db, "wm-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_watermark_timestamp_seconds", view(id, freq), float64(watermark.Unix()), seen)
		seedOverviewMetricForInstance(t, db, "arr-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_arrival_timestamp_seconds", view(id, freq), float64(watermark.Add(bar+time.Second).Unix()), seen)
	}
	query := openOverviewWithStorage(t, &goneMetadata{}, func(db *gorm.DB) {
		seedView(db, "hour_quiet", "1h", 40*time.Minute, time.Hour)
		seedView(db, "hour_dead", "1h", 2*time.Hour, time.Hour)
		seedView(db, "minute_quiet", "1m", 10*time.Minute, time.Minute)
	})
	got, err := (Builder{Metrics: query, Inventory: cryptoInventory, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
	require.NoError(t, err)
	reasons := map[string]string{}
	for _, item := range got.Datasets {
		reasons[item.DatasetID] = item.Reason
	}
	require.NotEqual(t, "producer stale", reasons["view_hour_quiet"], "一小时周期的视图静默 40 分钟是正常的")
	require.Equal(t, "producer stale", reasons["view_hour_dead"], "静默超过一个周期加宽限仍算停止上报")
	require.Equal(t, "producer stale", reasons["view_minute_quiet"], "分钟级视图沿用 2 分钟判定")
}
