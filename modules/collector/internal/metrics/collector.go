package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/model"
)

// Collector 指标收集器接口
type Collector interface {
	// 计数器
	IncCounter(name string, value int64, tags ...string)
	IncrementCounter(name string, tags map[string]string)
	GetCounter(name string) int64
	
	// 测量值
	SetGauge(name string, value float64, tags ...string)
	GetGauge(name string) float64
	
	// 直方图（用于响应时间等）
	RecordDuration(name string, duration time.Duration, tags ...string)
	
	// 获取所有指标
	GetMetrics() map[string]interface{}
	
	// 重置指标
	Reset()
}

// Config 指标配置
type Config struct {
	Enabled     bool `json:"enabled" yaml:"enabled"`
	ReportInterval time.Duration `json:"report_interval" yaml:"report_interval"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	Enabled:     true,
	ReportInterval: 30 * time.Second,
}

// Counter 计数器
type Counter struct {
	value int64
	tags  map[string]string
}

func (c *Counter) Inc(value int64) {
	atomic.AddInt64(&c.value, value)
}

func (c *Counter) Get() int64 {
	return atomic.LoadInt64(&c.value)
}

func (c *Counter) Reset() {
	atomic.StoreInt64(&c.value, 0)
}

// Gauge 测量值
type Gauge struct {
	value float64
	tags  map[string]string
	mu    sync.RWMutex
}

func (g *Gauge) Set(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value = value
}

func (g *Gauge) Get() float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.value
}

// Histogram 直方图（简化实现）
type Histogram struct {
	count    int64
	sum      int64 // 微秒
	min      int64
	max      int64
	tags     map[string]string
	mu       sync.RWMutex
}

func (h *Histogram) Record(duration time.Duration) {
	micros := duration.Microseconds()
	
	h.mu.Lock()
	defer h.mu.Unlock()
	
	atomic.AddInt64(&h.count, 1)
	atomic.AddInt64(&h.sum, micros)
	
	if h.min == 0 || micros < h.min {
		h.min = micros
	}
	if micros > h.max {
		h.max = micros
	}
}

func (h *Histogram) Stats() (count int64, avg float64, min, max time.Duration) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	count = atomic.LoadInt64(&h.count)
	if count > 0 {
		avg = float64(atomic.LoadInt64(&h.sum)) / float64(count)
	}
	min = time.Duration(h.min) * time.Microsecond
	max = time.Duration(h.max) * time.Microsecond
	
	return
}

// memoryCollector 内存指标收集器实现
type memoryCollector struct {
	config     Config
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	mu         sync.RWMutex
}

// New 创建新的指标收集器
func New(cfg Config) Collector {
	return &memoryCollector{
		config:     cfg,
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// NewDefault 创建默认指标收集器
func NewDefault() Collector {
	return New(DefaultConfig)
}

func (m *memoryCollector) IncCounter(name string, value int64, tags ...string) {
	if !m.config.Enabled {
		return
	}
	
	m.mu.Lock()
	counter, exists := m.counters[name]
	if !exists {
		counter = &Counter{
			tags: parseTags(tags),
		}
		m.counters[name] = counter
	}
	m.mu.Unlock()
	
	counter.Inc(value)
}

func (m *memoryCollector) IncrementCounter(name string, tags map[string]string) {
	if !m.config.Enabled {
		return
	}
	
	m.mu.Lock()
	counter, exists := m.counters[name]
	if !exists {
		counter = &Counter{
			tags: tags,
		}
		m.counters[name] = counter
	}
	m.mu.Unlock()
	
	counter.Inc(1)
}

func (m *memoryCollector) GetCounter(name string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if counter, exists := m.counters[name]; exists {
		return counter.Get()
	}
	return 0
}

func (m *memoryCollector) SetGauge(name string, value float64, tags ...string) {
	if !m.config.Enabled {
		return
	}
	
	m.mu.Lock()
	gauge, exists := m.gauges[name]
	if !exists {
		gauge = &Gauge{
			tags: parseTags(tags),
		}
		m.gauges[name] = gauge
	}
	m.mu.Unlock()
	
	gauge.Set(value)
}

func (m *memoryCollector) GetGauge(name string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if gauge, exists := m.gauges[name]; exists {
		return gauge.Get()
	}
	return 0
}

func (m *memoryCollector) RecordDuration(name string, duration time.Duration, tags ...string) {
	if !m.config.Enabled {
		return
	}
	
	m.mu.Lock()
	histogram, exists := m.histograms[name]
	if !exists {
		histogram = &Histogram{
			tags: parseTags(tags),
		}
		m.histograms[name] = histogram
	}
	m.mu.Unlock()
	
	histogram.Record(duration)
}

func (m *memoryCollector) GetMetrics() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	metrics := make(map[string]interface{})
	
	// 计数器
	counters := make(map[string]int64)
	for name, counter := range m.counters {
		counters[name] = counter.Get()
	}
	metrics["counters"] = counters
	
	// 测量值
	gauges := make(map[string]float64)
	for name, gauge := range m.gauges {
		gauges[name] = gauge.Get()
	}
	metrics["gauges"] = gauges
	
	// 直方图
	histograms := make(map[string]map[string]interface{})
	for name, histogram := range m.histograms {
		count, avg, min, max := histogram.Stats()
		histograms[name] = map[string]interface{}{
			"count": count,
			"avg_microseconds": avg,
			"min_microseconds": min.Microseconds(),
			"max_microseconds": max.Microseconds(),
		}
	}
	metrics["histograms"] = histograms
	
	return metrics
}

func (m *memoryCollector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	for _, counter := range m.counters {
		counter.Reset()
	}
	
	// 重新创建map以清空histogram
	m.histograms = make(map[string]*Histogram)
}

// parseTags 解析标签
func parseTags(tags []string) map[string]string {
	result := make(map[string]string)
	for i := 0; i < len(tags); i += 2 {
		if i+1 < len(tags) {
			result[tags[i]] = tags[i+1]
		}
	}
	return result
}

// 预定义指标名称
const (
	MetricTasksTotal        = "tasks_total"
	MetricTasksRunning      = "tasks_running"
	MetricTasksSuccess      = "tasks_success"
	MetricTasksFailed       = "tasks_failed"
	MetricTaskDuration      = "task_duration"
	
	MetricCollectionsTotal  = "collections_total"
	MetricCollectionsSuccess = "collections_success"
	MetricCollectionsFailed = "collections_failed"
	MetricCollectionDuration = "collection_duration"
	
	MetricHeartbeatsTotal   = "heartbeats_total"
	MetricHeartbeatsSuccess = "heartbeats_success"
	MetricHeartbeatsFailed  = "heartbeats_failed"
	
	MetricMemoryUsage       = "memory_usage_bytes"
	MetricCPUUsage          = "cpu_usage_percent"
	MetricGoroutines        = "goroutines_count"
)

// BuildNodeMetrics 构建节点指标
func BuildNodeMetrics(collector Collector) *model.NodeMetrics {
	metrics := &model.NodeMetrics{
		TaskCount:   int(collector.GetCounter(MetricTasksRunning)),
		ErrorCount:  collector.GetCounter(MetricTasksFailed),
		CPUUsage:    collector.GetGauge(MetricCPUUsage),
		MemoryUsage: collector.GetGauge(MetricMemoryUsage),
		Timestamp:   time.Now(),
	}
	
	// 计算成功率
	totalTasks := collector.GetCounter(MetricTasksTotal)
	successTasks := collector.GetCounter(MetricTasksSuccess)
	if totalTasks > 0 {
		metrics.SuccessRate = float64(successTasks) / float64(totalTasks) * 100
	}
	
	return metrics
}