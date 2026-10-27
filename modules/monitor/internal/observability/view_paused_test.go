package observability

import (
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 因子任务暂停时生产者把它的结果数据集标为 enabled=0：View 水位停在原地是预期的，不再检查；
// 没有这条标记（或标记为 1）的数据集的 View 照常检查。
func TestOverviewSkipsViewsOfDatasetsTheProducerNoLongerExpects(t *testing.T) {
	now := time.Date(2026, 10, 27, 6, 30, 0, 0, time.UTC)
	watermark := now.Add(-15 * time.Minute)
	labels := func(dataset, view string) string {
		return `{"dataset_id":"` + dataset + `","freq":"1m","space_id":"crypto","view_id":"` + view + `"}`
	}
	datasetLabels := func(dataset string) string {
		return `{"dataset_id":"` + dataset + `","freq":"1m","space_id":"crypto"}`
	}
	seed := func(withPausedMarker bool, otherLiveProducer ...bool) func(*gorm.DB) {
		return func(db *gorm.DB) {
			for id, item := range map[string][2]string{"paused": {"dataset_factor_paused", "view_factor_paused"}, "live": {"dataset_factor_live", "view_factor_live"}} {
				seedOverviewMetricForInstance(t, db, "wm-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_watermark_timestamp_seconds", labels(item[0], item[1]), float64(watermark.Unix()), now)
				seedOverviewMetricForInstance(t, db, "arr-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_arrival_timestamp_seconds", labels(item[0], item[1]), float64(watermark.Add(time.Minute).Unix()), now)
			}
			seedOverviewMetricForInstance(t, db, "live-enabled", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_enabled", datasetLabels("dataset_factor_live"), 1, now)
			if withPausedMarker {
				seedOverviewMetricForInstance(t, db, "paused-enabled", "factor-mgr", "factor-mgr@control", "moox_factor_dataset_enabled", datasetLabels("dataset_factor_paused"), 0, now)
			}
			if len(otherLiveProducer) > 0 && otherLiveProducer[0] {
				// 另一个生产者仍明确期望这个数据集：旧生产者留下的 0 不能盖过它。
				seedOverviewMetricForInstance(t, db, "paused-enabled-other", "collector", "collector@control", "moox_collector_dataset_enabled", datasetLabels("dataset_factor_paused"), 1, now)
			}
		}
	}
	views := func(query *monmetrics.QueryService) []string {
		got, err := (Builder{Metrics: query, Inventory: cryptoInventory, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
		require.NoError(t, err)
		var ids []string
		for _, item := range got.Datasets {
			if item.Producer == "storage_view" {
				ids = append(ids, item.DatasetID)
			}
		}
		return ids
	}

	withMarker := openOverviewWithStorage(t, &goneMetadata{}, seed(true))
	require.Equal(t, []string{"view_factor_live"}, views(withMarker))

	withoutMarker := openOverviewWithStorage(t, &goneMetadata{}, seed(false))
	require.ElementsMatch(t, []string{"view_factor_live", "view_factor_paused"}, views(withoutMarker))

	bothProducers := openOverviewWithStorage(t, &goneMetadata{}, seed(true, true))
	require.ElementsMatch(t, []string{"view_factor_live", "view_factor_paused"}, views(bothProducers), "只要还有生产者期望它，View 就照常检查")
}
