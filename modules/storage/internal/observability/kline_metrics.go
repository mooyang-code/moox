package observability

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// IsKlineDatasetID keeps freshness observations scoped to market K-line
// datasets. Other time-series datasets, including Monitor's own metrics,
// must not feed the K-line freshness loop.
func IsKlineDatasetID(datasetID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(datasetID)), "kline")
}

func IsKlineViewID(viewID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(viewID)), "kline")
}

const defaultKlineSeriesTag = "default"

// KlineObservation is the committed freshness fact for one canonical K-line
// subject. DatasetID is used by Primary observations and ViewID by View
// observations.
type KlineObservation struct {
	SpaceID     string
	DatasetID   string
	ViewID      string
	SubjectID   string
	Frequency   string
	SeriesTag   string
	DataTime    time.Time
	CommittedAt time.Time
}

// KlineMetrics exposes per-subject K-line freshness for Storage Primary and
// Storage View. It intentionally has no series cap: the Monitor reporter owns
// the downstream cardinality bound, while Storage records successful writes.
type KlineMetrics struct {
	primaryLastDataTime        *prometheus.GaugeVec
	primaryLastCommitTimestamp *prometheus.GaugeVec
	viewLastDataTime           *prometheus.GaugeVec
	viewLastCommitTimestamp    *prometheus.GaugeVec

	mu sync.Mutex
}

func NewKlineMetrics(registerer prometheus.Registerer) (*KlineMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("storage kline metrics registerer is nil")
	}
	metrics := &KlineMetrics{
		primaryLastDataTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_kline", Name: "last_data_time_seconds",
			Help: "Latest committed K-line business data time in Storage Primary.",
		}, []string{"space_id", "dataset_id", "subject_id", "freq", "series_tag"}),
		primaryLastCommitTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_kline", Name: "last_commit_timestamp_seconds",
			Help: "Latest successful K-line commit time in Storage Primary.",
		}, []string{"space_id", "dataset_id", "subject_id", "freq", "series_tag"}),
		viewLastDataTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view_kline", Name: "last_data_time_seconds",
			Help: "Latest committed K-line business data time in an active Storage View.",
		}, []string{"space_id", "view_id", "subject_id", "freq", "series_tag"}),
		viewLastCommitTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view_kline", Name: "last_commit_timestamp_seconds",
			Help: "Latest successful K-line commit time in an active Storage View.",
		}, []string{"space_id", "view_id", "subject_id", "freq", "series_tag"}),
	}

	var err error
	if metrics.primaryLastDataTime, err = registerOrReuse(registerer, metrics.primaryLastDataTime); err != nil {
		return nil, err
	}
	if metrics.primaryLastCommitTimestamp, err = registerOrReuse(registerer, metrics.primaryLastCommitTimestamp); err != nil {
		return nil, err
	}
	if metrics.viewLastDataTime, err = registerOrReuse(registerer, metrics.viewLastDataTime); err != nil {
		return nil, err
	}
	if metrics.viewLastCommitTimestamp, err = registerOrReuse(registerer, metrics.viewLastCommitTimestamp); err != nil {
		return nil, err
	}
	return metrics, nil
}

// ObservePrimary records a successfully committed Primary observation.
func (m *KlineMetrics) ObservePrimary(observation KlineObservation) error {
	if m == nil {
		return fmt.Errorf("storage kline metrics are nil")
	}
	labels, err := canonicalKlineLabels(observation, false)
	if err != nil {
		return err
	}
	return m.observe(labels, m.primaryLastDataTime, m.primaryLastCommitTimestamp)
}

// ObserveView records a successfully committed active View observation.
func (m *KlineMetrics) ObserveView(observation KlineObservation) error {
	if m == nil {
		return fmt.Errorf("storage kline metrics are nil")
	}
	labels, err := canonicalKlineLabels(observation, true)
	if err != nil {
		return err
	}
	return m.observe(labels, m.viewLastDataTime, m.viewLastCommitTimestamp)
}

type klineLabels struct {
	values      []string
	dataTime    float64
	committedAt float64
}

func canonicalKlineLabels(observation KlineObservation, view bool) (klineLabels, error) {
	spaceID := strings.TrimSpace(observation.SpaceID)
	subjectID := strings.TrimSpace(observation.SubjectID)
	frequency := strings.TrimSpace(observation.Frequency)
	seriesTag := strings.TrimSpace(observation.SeriesTag)
	if seriesTag == "" {
		seriesTag = defaultKlineSeriesTag
	}
	if spaceID == "" || subjectID == "" || frequency == "" {
		return klineLabels{}, fmt.Errorf("storage kline observation requires space_id, subject_id, and freq")
	}
	if _, err := parseDatasetFrequency(frequency); err != nil {
		return klineLabels{}, fmt.Errorf("storage kline observation freq %q is invalid", frequency)
	}
	if observation.DataTime.IsZero() {
		return klineLabels{}, fmt.Errorf("storage kline observation data_time is required")
	}
	if observation.CommittedAt.IsZero() {
		return klineLabels{}, fmt.Errorf("storage kline observation committed_at is required")
	}

	var target string
	if view {
		target = strings.TrimSpace(observation.ViewID)
		if target == "" || strings.TrimSpace(observation.DatasetID) != "" {
			return klineLabels{}, fmt.Errorf("storage kline view observation requires only view_id")
		}
	} else {
		target = strings.TrimSpace(observation.DatasetID)
		if target == "" || strings.TrimSpace(observation.ViewID) != "" {
			return klineLabels{}, fmt.Errorf("storage kline primary observation requires only dataset_id")
		}
	}

	return klineLabels{
		values:      []string{spaceID, target, subjectID, frequency, seriesTag},
		dataTime:    timestampSeconds(observation.DataTime),
		committedAt: timestampSeconds(observation.CommittedAt),
	}, nil
}

func (m *KlineMetrics) observe(labels klineLabels, dataTime, committedAt *prometheus.GaugeVec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := setGaugeMonotonic(dataTime, labels.values, labels.dataTime); err != nil {
		return fmt.Errorf("observe kline data_time: %w", err)
	}
	if err := setGaugeMonotonic(committedAt, labels.values, labels.committedAt); err != nil {
		return fmt.Errorf("observe kline commit timestamp: %w", err)
	}
	return nil
}

func setGaugeMonotonic(gauge *prometheus.GaugeVec, labels []string, value float64) error {
	metric, err := gauge.GetMetricWithLabelValues(labels...)
	if err != nil {
		return err
	}
	var current dto.Metric
	if err := metric.Write(&current); err != nil {
		return err
	}
	if current.GetGauge().GetValue() < value {
		gauge.WithLabelValues(labels...).Set(value)
	}
	return nil
}

func timestampSeconds(value time.Time) float64 {
	return float64(value.UnixNano()) / float64(time.Second)
}
