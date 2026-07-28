package observability

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

const MaxDatasetSeries = 1000

// DatasetMetrics tracks the bounded union of time-series tuples this Storage
// process has actually attempted to commit. It is not an enabled-dataset
// inventory; Collector and Factor own that configuration-derived inventory.
type DatasetMetrics struct {
	inner    *report.DatasetMetrics
	maxItems int

	mu       sync.Mutex
	expected map[report.DatasetKey]time.Duration
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
		inner: inner, maxItems: maxItems, expected: make(map[report.DatasetKey]time.Duration),
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
	if _, ok := m.expected[observation.Key]; !ok {
		if len(m.expected) >= m.maxItems {
			return fmt.Errorf("storage dataset metric series limit %d exceeded", m.maxItems)
		}
		next := make([]report.DatasetExpectation, 0, len(m.expected)+1)
		for key, expectedInterval := range m.expected {
			next = append(next, report.DatasetExpectation{Key: key, Interval: expectedInterval})
		}
		next = append(next, report.DatasetExpectation{Key: observation.Key, Interval: interval})
		if err := m.inner.ReplaceExpected(next); err != nil {
			return fmt.Errorf("register storage dataset tuple: %w", err)
		}
		m.expected[observation.Key] = interval
	}
	return m.inner.ObserveRun(observation)
}

func parseDatasetFrequency(freq string) (time.Duration, error) {
	freq = strings.TrimSpace(freq)
	if len(freq) < 2 {
		return 0, fmt.Errorf("storage dataset freq %q is invalid", freq)
	}
	count, err := strconv.ParseUint(freq[:len(freq)-1], 10, 64)
	if err != nil || count == 0 {
		return 0, fmt.Errorf("storage dataset freq %q is invalid", freq)
	}
	var unit time.Duration
	switch freq[len(freq)-1] {
	case 'm':
		unit = time.Minute
	case 'h', 'H':
		unit = time.Hour
	case 'd', 'D':
		unit = 24 * time.Hour
	case 'w', 'W':
		unit = 7 * 24 * time.Hour
	case 'M':
		unit = 30 * 24 * time.Hour
	case 'y', 'Y':
		unit = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("storage dataset freq %q is invalid", freq)
	}
	if count > uint64((1<<63-1)/unit) {
		return 0, fmt.Errorf("storage dataset freq %q overflows duration", freq)
	}
	return time.Duration(count) * unit, nil
}
