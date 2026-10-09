package accessproxy

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusMetrics 把拒绝与转发结果记到 Prometheus。
type PrometheusMetrics struct {
	rejected *prometheus.CounterVec
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewPrometheusMetrics 创建并注册外部接入的指标。
func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	metrics := &PrometheusMetrics{
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_access_rejected_total",
			Help: "外部接入拒绝的请求数，按外部调用方和原因区分。",
		}, []string{"principal", "reason"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_access_requests_total",
			Help: "外部接入处理的请求数，按外部调用方、服务、方法和返回码区分。",
		}, []string{"principal", "service", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "moox_access_request_duration_seconds",
			Help:    "外部接入处理请求的耗时。",
			Buckets: prometheus.DefBuckets,
		}, []string{"principal", "service"}),
	}
	for _, collector := range []prometheus.Collector{metrics.rejected, metrics.requests, metrics.duration} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

// Rejected 记录一次拒绝。
func (m *PrometheusMetrics) Rejected(principal, reason string) {
	m.rejected.WithLabelValues(principal, reason).Inc()
}

// Forwarded 记录一次请求的结果与耗时。
func (m *PrometheusMetrics) Forwarded(principal, servicePath, method string, code int, elapsed time.Duration) {
	m.requests.WithLabelValues(principal, servicePath, method, strconv.Itoa(code)).Inc()
	m.duration.WithLabelValues(principal, servicePath).Observe(elapsed.Seconds())
}
