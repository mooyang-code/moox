package events

// Storage View consumer names are part of the EventBus topology contract.
// Keep them in the events package so Storage, Admin and operational tooling
// cannot silently drift to different durable names.
const (
	StorageViewConsumerStream  = "MOOX_STORAGE"
	StorageViewFactorConsumer  = "storage_view_factor"
	StorageViewMetricsConsumer = "storage_view_metrics"
	StorageViewMiscConsumer    = "storage_view_misc"

)

var StorageViewConsumerDurables = []string{
	StorageViewFactorConsumer,
	StorageViewMetricsConsumer,
	StorageViewMiscConsumer,
}
