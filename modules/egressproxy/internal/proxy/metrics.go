package proxy

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 记录出口代理的 HTTP 请求结果。为空时不记录。
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	rejects  *prometheus.CounterVec
}

// NewMetrics 创建并注册出口代理的 HTTP 指标。
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_egress_http_requests_total",
			Help: "出口代理发出的 HTTPS 请求数，按目标域名和结果（HTTP 状态码、error、too_large）区分。",
		}, []string{"host", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "moox_egress_http_request_duration_seconds",
			Help:    "出口代理发出 HTTPS 请求的耗时。",
			Buckets: prometheus.DefBuckets,
		}, []string{"host"}),
		rejects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_egress_http_rejected_total",
			Help: "出口代理拒绝的请求数，按原因区分。",
		}, []string{"reason"}),
	}
	for _, collector := range []prometheus.Collector{m.requests, m.duration, m.rejects} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Metrics) forwarded(host, result string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(host, result).Inc()
	m.duration.WithLabelValues(host).Observe(elapsed.Seconds())
}

func (m *Metrics) rejected(reason string) {
	if m != nil {
		m.rejects.WithLabelValues(reason).Inc()
	}
}
