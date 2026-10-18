package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CleanupMetrics records the retention cleanup that storage-primary runs for
// each time-series Dataset.
type CleanupMetrics struct {
	cutoff        *prometheus.GaugeVec
	duration      *prometheus.GaugeVec
	deletedRanges *prometheus.CounterVec
	failures      *prometheus.CounterVec
}

func NewCleanupMetrics(registerer prometheus.Registerer) (*CleanupMetrics, error) {
	labels := []string{"space_id", "dataset_id"}
	cutoff, err := registerOrReuse(registerer, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "moox", Subsystem: "storage", Name: "dataset_cleanup_cutoff_timestamp_seconds",
		Help: "Rows of a Dataset older than this time have been deleted by the last cleanup.",
	}, labels))
	if err != nil {
		return nil, err
	}
	duration, err := registerOrReuse(registerer, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "moox", Subsystem: "storage", Name: "dataset_cleanup_duration_seconds",
		Help: "Duration of the last retention cleanup of a Dataset.",
	}, labels))
	if err != nil {
		return nil, err
	}
	deletedRanges, err := registerOrReuse(registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "moox", Subsystem: "storage", Name: "dataset_cleanup_deleted_ranges_total",
		Help: "Key ranges deleted by retention cleanup.",
	}, labels))
	if err != nil {
		return nil, err
	}
	failures, err := registerOrReuse(registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "moox", Subsystem: "storage", Name: "dataset_cleanup_failures_total",
		Help: "Retention cleanups of a Dataset that failed.",
	}, labels))
	if err != nil {
		return nil, err
	}
	return &CleanupMetrics{cutoff: cutoff, duration: duration, deletedRanges: deletedRanges, failures: failures}, nil
}

// ObserveCleanup records one successful cleanup of a Dataset.
func (m *CleanupMetrics) ObserveCleanup(spaceID, datasetID string, cutoff time.Time, elapsed time.Duration, deletedRanges uint64) {
	if m == nil {
		return
	}
	m.cutoff.WithLabelValues(spaceID, datasetID).Set(float64(cutoff.Unix()))
	m.duration.WithLabelValues(spaceID, datasetID).Set(elapsed.Seconds())
	m.deletedRanges.WithLabelValues(spaceID, datasetID).Add(float64(deletedRanges))
}

// ObserveFailure records a failed cleanup of a Dataset.
func (m *CleanupMetrics) ObserveFailure(spaceID, datasetID string) {
	if m == nil {
		return
	}
	m.failures.WithLabelValues(spaceID, datasetID).Inc()
}

// RegisterPebbleDiskUsage exports the on-disk size of a DataNode's Pebble
// store, read on every scrape.
func RegisterPebbleDiskUsage(registerer prometheus.Registerer, nodeID string, usage func() uint64) error {
	_, err := registerOrReuse(registerer, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "moox", Subsystem: "storage_node", Name: "pebble_disk_bytes",
		Help:        "On-disk size of the DataNode Pebble store, including obsolete files awaiting deletion.",
		ConstLabels: prometheus.Labels{"node_id": nodeID},
	}, func() float64 { return float64(usage()) }))
	return err
}
