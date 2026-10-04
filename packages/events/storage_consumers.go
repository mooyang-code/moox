package events

// Storage View consumer names are part of the EventBus topology contract.
// Keep them in the events package so Storage, Admin and operational tooling
// cannot silently drift to different durable names.
const (
	StorageViewConsumerStream  = "MOOX_STORAGE"
	StorageViewKlineConsumer   = "storage_view_kline"
	StorageViewFactorConsumer  = "storage_view_factor"
	StorageViewMetricsConsumer = "storage_view_metrics"
	StorageViewMiscConsumer    = "storage_view_misc"

	// The versioned names are retained only for one-time migration/reset
	// tooling. New deployments must use the unversioned names above.
	StorageViewLegacyKlineConsumer   = "storage_view_kline_v2"
	StorageViewLegacyFactorConsumer  = "storage_view_factor_v1"
	StorageViewLegacyMetricsConsumer = "storage_view_metrics_v2"
	StorageViewLegacyMiscConsumer    = "storage_view_misc_v1"
	StorageViewLegacyOtherConsumer   = "storage_view_other_v2"
	StorageViewLegacyConsumer        = "storage_view_period_v1"
	StorageViewLegacyBroadConsumer   = "storage_view"
)

var StorageViewConsumerDurables = []string{
	StorageViewKlineConsumer,
	StorageViewFactorConsumer,
	StorageViewMetricsConsumer,
	StorageViewMiscConsumer,
}
