package events

// Storage View consumer names are part of the EventBus topology contract.
// Keep them in the events package so Storage, Admin and operational tooling
// cannot silently drift to different durable names.
const (
	StorageViewConsumerStream      = "MOOX_STORAGE"
	StorageViewKlineConsumer       = "storage_view_kline_v2"
	StorageViewMetricsConsumer     = "storage_view_metrics_v2"
	StorageViewOtherConsumer       = "storage_view_other_v2"
	StorageViewLegacyConsumer      = "storage_view_period_v1"
	StorageViewLegacyBroadConsumer = "storage_view"
)

var StorageViewConsumerDurables = []string{
	StorageViewKlineConsumer,
	StorageViewMetricsConsumer,
	StorageViewOtherConsumer,
}
