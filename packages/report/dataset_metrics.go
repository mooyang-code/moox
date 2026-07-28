package report

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var allowedDatasetResults = stringSet("success", "error", "empty", "rejected")

type DatasetKey struct {
	SpaceID   string
	DatasetID string
	Freq      string
}

type DatasetExpectation struct {
	Key      DatasetKey
	Interval time.Duration
}

type DatasetObservation struct {
	Key             DatasetKey
	Result          string
	Rows            uint64
	FinishedAt      time.Time
	InputWatermark  time.Time
	OutputWatermark time.Time
}

type DatasetMetrics struct {
	enabled                  *prometheus.GaugeVec
	expectedInterval         *prometheus.GaugeVec
	inventoryRefreshErrors   prometheus.Counter
	inventoryLastSuccess     prometheus.Gauge
	runs                     *prometheus.CounterVec
	lastRun                  *prometheus.GaugeVec
	lastSuccess              *prometheus.GaugeVec
	inputWatermark           *prometheus.GaugeVec
	outputWatermark          *prometheus.GaugeVec
	rows                     *prometheus.CounterVec
	mu                       sync.Mutex
	expected                 map[DatasetKey]time.Duration
	inputWatermarkByDataset  map[DatasetKey]float64
	outputWatermarkByDataset map[DatasetKey]float64
}

func NewDatasetMetrics(registerer prometheus.Registerer, module string) (*DatasetMetrics, error) {
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	prefix := "moox_" + module + "_dataset_"
	labels := []string{"space_id", "dataset_id", "freq"}
	m := &DatasetMetrics{
		enabled:                  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "enabled", Help: "Whether a realtime dataset is expected to run."}, labels),
		expectedInterval:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "expected_interval_seconds", Help: "Expected realtime dataset collection interval."}, labels),
		inventoryRefreshErrors:   prometheus.NewCounter(prometheus.CounterOpts{Name: prefix + "inventory_refresh_errors_total", Help: "Failed expected dataset inventory replacements."}),
		inventoryLastSuccess:     prometheus.NewGauge(prometheus.GaugeOpts{Name: prefix + "inventory_last_success_timestamp_seconds", Help: "Unix timestamp of the last successful expected dataset inventory replacement."}),
		runs:                     prometheus.NewCounterVec(prometheus.CounterOpts{Name: prefix + "runs_total", Help: "Completed realtime dataset runs."}, append(labels, "result")),
		lastRun:                  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "last_run_timestamp_seconds", Help: "Unix timestamp of the last completed realtime dataset run."}, labels),
		lastSuccess:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "last_success_timestamp_seconds", Help: "Unix timestamp of the last successful realtime dataset run."}, labels),
		inputWatermark:           prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "input_watermark_timestamp_seconds", Help: "Latest observed input business watermark."}, labels),
		outputWatermark:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "output_watermark_timestamp_seconds", Help: "Latest observed output business watermark."}, labels),
		rows:                     prometheus.NewCounterVec(prometheus.CounterOpts{Name: prefix + "rows_total", Help: "Rows produced by realtime dataset runs."}, append(labels, "result")),
		expected:                 make(map[DatasetKey]time.Duration),
		inputWatermarkByDataset:  make(map[DatasetKey]float64),
		outputWatermarkByDataset: make(map[DatasetKey]float64),
	}
	collectors := []prometheus.Collector{
		m.enabled, m.expectedInterval, m.inventoryRefreshErrors, m.inventoryLastSuccess,
		m.runs, m.lastRun, m.lastSuccess, m.inputWatermark, m.outputWatermark, m.rows,
	}
	registered := make([]prometheus.Collector, 0, len(collectors))
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			for _, item := range registered {
				registerer.Unregister(item)
			}
			return nil, fmt.Errorf("register dataset metrics: %w", err)
		}
		registered = append(registered, collector)
	}
	return m, nil
}

func (m *DatasetMetrics) ReplaceExpected(items []DatasetExpectation) error {
	if m == nil {
		return fmt.Errorf("dataset metrics are nil")
	}
	next := make(map[DatasetKey]time.Duration, len(items))
	for i, item := range items {
		if err := validateDatasetKey(item.Key); err != nil {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("expected dataset %d: %w", i, err)
		}
		if item.Interval <= 0 {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("expected dataset %d interval must be positive", i)
		}
		if _, exists := next[item.Key]; exists {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("duplicate expected dataset %s/%s/%s", item.Key.SpaceID, item.Key.DatasetID, item.Key.Freq)
		}
		next[item.Key] = item.Interval
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled.Reset()
	m.expectedInterval.Reset()
	for key, interval := range next {
		values := datasetLabelValues(key)
		m.enabled.WithLabelValues(values...).Set(1)
		m.expectedInterval.WithLabelValues(values...).Set(interval.Seconds())
	}
	m.expected = next
	m.inventoryLastSuccess.Set(float64(time.Now().UTC().Unix()))
	return nil
}

// ObserveInventoryRefreshError records failures that happen before a complete
// replacement set can be built. ReplaceExpected records its own validation
// failures, so callers should not call both for the same error.
func (m *DatasetMetrics) ObserveInventoryRefreshError() {
	if m != nil {
		m.inventoryRefreshErrors.Inc()
	}
}

func (m *DatasetMetrics) ObserveRun(observation DatasetObservation) error {
	if m == nil {
		return fmt.Errorf("dataset metrics are nil")
	}
	if err := validateDatasetKey(observation.Key); err != nil {
		return err
	}
	if !allowedDatasetResults[observation.Result] {
		return fmt.Errorf("unknown dataset result %q", observation.Result)
	}
	if observation.FinishedAt.IsZero() {
		return fmt.Errorf("dataset finished_at is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.expected[observation.Key]; !ok {
		return fmt.Errorf("dataset %s/%s/%s is not expected", observation.Key.SpaceID, observation.Key.DatasetID, observation.Key.Freq)
	}
	values := datasetLabelValues(observation.Key)
	resultValues := append(append([]string(nil), values...), observation.Result)
	m.runs.WithLabelValues(resultValues...).Inc()
	m.rows.WithLabelValues(resultValues...).Add(float64(observation.Rows))
	finishedAt := float64(observation.FinishedAt.UTC().Unix())
	m.lastRun.WithLabelValues(values...).Set(finishedAt)
	if observation.Result == "success" {
		m.lastSuccess.WithLabelValues(values...).Set(finishedAt)
	}
	m.advanceWatermark(m.inputWatermark, m.inputWatermarkByDataset, observation.Key, observation.InputWatermark)
	m.advanceWatermark(m.outputWatermark, m.outputWatermarkByDataset, observation.Key, observation.OutputWatermark)
	return nil
}

func (m *DatasetMetrics) advanceWatermark(metric *prometheus.GaugeVec, values map[DatasetKey]float64, key DatasetKey, watermark time.Time) {
	if watermark.IsZero() {
		return
	}
	seconds := float64(watermark.UTC().Unix())
	if previous, ok := values[key]; ok && seconds <= previous {
		return
	}
	values[key] = seconds
	metric.WithLabelValues(datasetLabelValues(key)...).Set(seconds)
}

func validateDatasetKey(key DatasetKey) error {
	if err := validateMetricLabel("space_id", key.SpaceID); err != nil {
		return err
	}
	if err := validateMetricLabel("dataset_id", key.DatasetID); err != nil {
		return err
	}
	if err := validateMetricLabel("freq", key.Freq); err != nil {
		return err
	}
	return nil
}

func datasetLabelValues(key DatasetKey) []string {
	return []string{key.SpaceID, key.DatasetID, key.Freq}
}
