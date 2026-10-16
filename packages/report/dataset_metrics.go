package report

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/prometheus/client_golang/prometheus"
)

const MaxDatasetMetricSeries = 1000

// MaxDatasetNameBytes 是数据集展示名称的字节上限，超出按字符截断：名称只用于告警标题，远低于上报的标签值上限（512 字节），
// 一个过长的名称不能让整份指标快照被拒收。
const MaxDatasetNameBytes = 192

// partial 是按设计只产出部分结果的运行（窗口空洞保护、历史不足、预热中）：生产者在正常工作，只是数据还在积累，
// Monitor 据此显示“积累中”而不告警；incomplete 才是意外的不完整，仍按失败处理。
var allowedDatasetResults = stringSet("success", "error", "empty", "rejected", "incomplete", "partial")

type DatasetKey struct {
	SpaceID   string
	DatasetID string
	Freq      string
}

type DatasetExpectation struct {
	Key      DatasetKey
	Interval time.Duration
	// Name 是数据集给人看的名称（例如策略实例名），Monitor 用它命名该数据集的新鲜度检查；为空时 Monitor 退回 Storage
	// 里登记的数据集名称或数据集 ID。名称不是数据集身份，可以随时变化。
	Name string
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
	enabled                *prometheus.GaugeVec
	expectedInterval       *prometheus.GaugeVec
	info                   *prometheus.GaugeVec
	inventoryRefreshErrors prometheus.Counter
	inventoryLastSuccess   prometheus.Gauge
	runs                   *prometheus.CounterVec
	lastRun                *prometheus.GaugeVec
	lastSuccess            *prometheus.GaugeVec
	lastPartial            *prometheus.GaugeVec
	inputWatermark         *prometheus.GaugeVec
	outputWatermark        *prometheus.GaugeVec
	// outputArrival 是输出水位最近一次前进时的墙上时间（这批数据写入完成的时刻），Monitor 用它算最新一根 K 线的到达延迟。
	outputArrival            *prometheus.GaugeVec
	rows                     *prometheus.CounterVec
	mu                       sync.Mutex
	expected                 map[DatasetKey]time.Duration
	known                    map[DatasetKey]struct{}
	names                    map[DatasetKey]string
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
		info:                     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "info", Help: "期望数据集的展示名称，值恒为 1；只列出当前清单中有名称的数据集。"}, append(append([]string(nil), labels...), "name")),
		inventoryRefreshErrors:   prometheus.NewCounter(prometheus.CounterOpts{Name: prefix + "inventory_refresh_errors_total", Help: "Failed expected dataset inventory replacements."}),
		inventoryLastSuccess:     prometheus.NewGauge(prometheus.GaugeOpts{Name: prefix + "inventory_last_success_timestamp_seconds", Help: "Unix timestamp of the last successful expected dataset inventory replacement."}),
		runs:                     prometheus.NewCounterVec(prometheus.CounterOpts{Name: prefix + "runs_total", Help: "Completed realtime dataset runs."}, append(labels, "result")),
		lastRun:                  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "last_run_timestamp_seconds", Help: "Unix timestamp of the last completed realtime dataset run."}, labels),
		lastSuccess:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "last_success_timestamp_seconds", Help: "Unix timestamp of the last successful realtime dataset run."}, labels),
		lastPartial:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "last_partial_timestamp_seconds", Help: "Unix timestamp of the last realtime dataset run that produced only partial results by design (data still accumulating)."}, labels),
		inputWatermark:           prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "input_watermark_timestamp_seconds", Help: "Latest observed input business watermark."}, labels),
		outputWatermark:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "output_watermark_timestamp_seconds", Help: "Latest observed output business watermark."}, labels),
		outputArrival:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: prefix + "output_arrival_timestamp_seconds", Help: "输出水位最近一次前进时的墙上时间（这批数据写入完成的时刻）。"}, labels),
		rows:                     prometheus.NewCounterVec(prometheus.CounterOpts{Name: prefix + "rows_total", Help: "Rows produced by realtime dataset runs."}, append(labels, "result")),
		expected:                 make(map[DatasetKey]time.Duration),
		known:                    make(map[DatasetKey]struct{}),
		inputWatermarkByDataset:  make(map[DatasetKey]float64),
		outputWatermarkByDataset: make(map[DatasetKey]float64),
	}
	collectors := []prometheus.Collector{
		m.enabled, m.expectedInterval, m.info, m.inventoryRefreshErrors, m.inventoryLastSuccess,
		m.runs, m.lastRun, m.lastSuccess, m.lastPartial, m.inputWatermark, m.outputWatermark, m.outputArrival, m.rows,
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
	names := make(map[DatasetKey]string, len(items))
	for i, item := range items {
		key, err := canonicalDatasetKey(item.Key)
		if err != nil {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("expected dataset %d: %w", i, err)
		}
		if item.Interval <= 0 {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("expected dataset %d interval must be positive", i)
		}
		if _, exists := next[key]; exists {
			m.inventoryRefreshErrors.Inc()
			return fmt.Errorf("duplicate expected dataset %s/%s/%s", key.SpaceID, key.DatasetID, key.Freq)
		}
		next[key] = item.Interval
		if name := datasetDisplayName(item.Name); name != "" {
			names[key] = name
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	known := make(map[DatasetKey]struct{}, len(m.known)+len(next))
	for key := range m.known {
		known[key] = struct{}{}
	}
	for key := range next {
		known[key] = struct{}{}
	}
	if len(known) > MaxDatasetMetricSeries {
		m.inventoryRefreshErrors.Inc()
		return fmt.Errorf("dataset metric series exceed limit %d", MaxDatasetMetricSeries)
	}
	for key := range m.known {
		if _, remains := next[key]; remains {
			continue
		}
		values := datasetLabelValues(key)
		m.enabled.WithLabelValues(values...).Set(0)
		m.expectedInterval.WithLabelValues(values...).Set(0)
	}
	for key, interval := range next {
		values := datasetLabelValues(key)
		m.enabled.WithLabelValues(values...).Set(1)
		m.expectedInterval.WithLabelValues(values...).Set(interval.Seconds())
	}
	// 改名或移出清单（含失去名称）的数据集删除旧名称序列，旧名称随即不再上报，Monitor 只认仍在上报的那条。
	for key, name := range m.names {
		if names[key] != name {
			m.info.DeleteLabelValues(append(datasetLabelValues(key), name)...)
		}
	}
	for key, name := range names {
		m.info.WithLabelValues(append(datasetLabelValues(key), name)...).Set(1)
	}
	m.names = names
	m.expected = next
	m.known = known
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
	return m.observeRun(observation, true)
}

// ObserveFact records an authoritative commit fact without declaring that the
// reporting module owns the enabled Dataset inventory.
func (m *DatasetMetrics) ObserveFact(observation DatasetObservation) error {
	return m.observeRun(observation, false)
}

func (m *DatasetMetrics) observeRun(observation DatasetObservation, requireExpected bool) error {
	if m == nil {
		return fmt.Errorf("dataset metrics are nil")
	}
	key, err := canonicalDatasetKey(observation.Key)
	if err != nil {
		return err
	}
	observation.Key = key
	if !allowedDatasetResults[observation.Result] {
		return fmt.Errorf("unknown dataset result %q", observation.Result)
	}
	if observation.FinishedAt.IsZero() {
		return fmt.Errorf("dataset finished_at is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.expected[observation.Key]; requireExpected && !ok {
		return fmt.Errorf("dataset %s/%s/%s is not expected", observation.Key.SpaceID, observation.Key.DatasetID, observation.Key.Freq)
	}
	values := datasetLabelValues(observation.Key)
	resultValues := append(append([]string(nil), values...), observation.Result)
	m.runs.WithLabelValues(resultValues...).Inc()
	m.rows.WithLabelValues(resultValues...).Add(float64(observation.Rows))
	finishedAt := float64(observation.FinishedAt.UTC().Unix())
	m.lastRun.WithLabelValues(values...).Set(finishedAt)
	if observation.Result == "success" || observation.Result == "empty" {
		m.lastSuccess.WithLabelValues(values...).Set(finishedAt)
	}
	if observation.Result == "partial" {
		m.lastPartial.WithLabelValues(values...).Set(finishedAt)
	}
	m.advanceWatermark(m.inputWatermark, m.inputWatermarkByDataset, observation.Key, observation.InputWatermark)
	if m.advanceWatermark(m.outputWatermark, m.outputWatermarkByDataset, observation.Key, observation.OutputWatermark) {
		m.outputArrival.WithLabelValues(values...).Set(finishedAt)
	}
	return nil
}

// advanceWatermark 只让水位前进，返回本次是否前进了。
func (m *DatasetMetrics) advanceWatermark(metric *prometheus.GaugeVec, values map[DatasetKey]float64, key DatasetKey, watermark time.Time) bool {
	if watermark.IsZero() {
		return false
	}
	seconds := float64(watermark.UTC().Unix())
	if previous, ok := values[key]; ok && seconds <= previous {
		return false
	}
	values[key] = seconds
	metric.WithLabelValues(datasetLabelValues(key)...).Set(seconds)
	return true
}

func validateDatasetKey(key DatasetKey) error {
	if err := validateMetricLabel("space_id", key.SpaceID); err != nil {
		return err
	}
	if err := validateMetricLabel("dataset_id", key.DatasetID); err != nil {
		return err
	}
	if err := validateDatasetFrequency(key.Freq); err != nil {
		return err
	}
	return nil
}

func canonicalDatasetKey(key DatasetKey) (DatasetKey, error) {
	if err := validateDatasetKey(key); err != nil {
		return DatasetKey{}, err
	}
	freq, err := frequency.Parse(key.Freq)
	if err != nil {
		return DatasetKey{}, err
	}
	key.Freq = string(freq)
	return key, nil
}

func validateDatasetFrequency(value string) error {
	if len(value) > 64 {
		return fmt.Errorf("invalid freq %q", value)
	}
	if _, err := frequency.Parse(value); err != nil {
		return fmt.Errorf("invalid freq %q", value)
	}
	return nil
}

// datasetDisplayName 规整展示名称：去掉首尾空白和非法 UTF-8（标签值必须是合法 UTF-8），超长时按字符截断。
func datasetDisplayName(name string) string {
	name = strings.TrimSpace(strings.ToValidUTF8(name, ""))
	if len(name) <= MaxDatasetNameBytes {
		return name
	}
	const ellipsis = "…"
	cut := MaxDatasetNameBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return strings.TrimSpace(name[:cut]) + ellipsis
}

func datasetLabelValues(key DatasetKey) []string {
	return []string{key.SpaceID, key.DatasetID, key.Freq}
}
