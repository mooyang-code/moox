package observability

import (
	"fmt"
	"sync"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

const MaxDatasetSeries = 1000

// DatasetMetrics tracks the bounded union of time-series tuples this Storage
// process has actually attempted to commit. It records facts only; Collector
// and Factor own the enabled-dataset inventory.
type DatasetMetrics struct {
	inner    *report.DatasetMetrics
	maxItems int

	mu    sync.Mutex
	known map[report.DatasetKey]time.Duration
}

func NewDatasetMetrics(registerer prometheus.Registerer) (*DatasetMetrics, error) {
	return newDatasetMetrics(registerer, MaxDatasetSeries)
}

func newDatasetMetrics(registerer prometheus.Registerer, maxItems int) (*DatasetMetrics, error) {
	if maxItems <= 0 {
		return nil, fmt.Errorf("storage dataset metrics max series must be positive")
	}
	inner, err := report.NewDatasetMetrics(registerer, "storage")
	if err != nil {
		return nil, err
	}
	return &DatasetMetrics{
		inner: inner, maxItems: maxItems, known: make(map[report.DatasetKey]time.Duration),
	}, nil
}

func (m *DatasetMetrics) ObserveRun(observation report.DatasetObservation) error {
	if m == nil || m.inner == nil {
		return fmt.Errorf("storage dataset metrics are nil")
	}
	interval, err := parseDatasetFrequency(observation.Key.Freq)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.known[observation.Key]; !ok {
		if len(m.known) >= m.maxItems {
			return fmt.Errorf("storage dataset metric series limit %d exceeded", m.maxItems)
		}
		m.known[observation.Key] = interval
	}
	return m.inner.ObserveFact(observation)
}

func parseDatasetFrequency(freq string) (time.Duration, error) {
	parsed, err := frequencypkg.Parse(freq)
	if err != nil {
		return 0, fmt.Errorf("storage dataset freq %q is invalid", freq)
	}
	return parsed.NominalDuration(), nil
}
