package resolver

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 记录 DNS 解析的健康状况，不放进 RPC 响应。可选：单元测试不需要 Prometheus。
type Metrics struct {
	Requests       prometheus.Counter
	Failures       prometheus.Counter
	Unresolved     prometheus.Counter
	ProbeFailures  prometheus.Counter
	LookupDuration prometheus.Histogram
	ProbeDuration  prometheus.Histogram
}

// NewMetrics 创建并注册出口代理的 DNS 解析指标。
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Requests: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_egress_dns_requests_total",
			Help: "出口代理收到的 ResolveDomains 请求数。",
		}),
		Failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_egress_dns_failures_total",
			Help: "整个请求失败的 ResolveDomains 次数。",
		}),
		Unresolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_egress_dns_unresolved_domains_total",
			Help: "没有得到可用地址的域名数。",
		}),
		ProbeFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_egress_dns_probe_failures_total",
			Help: "候选地址 TCP 探测失败的次数。",
		}),
		LookupDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "moox_egress_dns_lookup_duration_seconds",
			Help: "出口代理所在主机上的 DNS 查询耗时。",
		}),
		ProbeDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "moox_egress_dns_probe_duration_seconds",
			Help: "一批候选地址 TCP 探测的耗时。",
		}),
	}
	for _, collector := range []prometheus.Collector{
		m.Requests, m.Failures, m.Unresolved, m.ProbeFailures, m.LookupDuration, m.ProbeDuration,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Metrics) observeLookup(duration time.Duration) {
	if m != nil && m.LookupDuration != nil {
		m.LookupDuration.Observe(duration.Seconds())
	}
}

func (m *Metrics) observeProbe(duration time.Duration, failures int) {
	if m == nil {
		return
	}
	if m.ProbeDuration != nil {
		m.ProbeDuration.Observe(duration.Seconds())
	}
	if failures > 0 && m.ProbeFailures != nil {
		m.ProbeFailures.Add(float64(failures))
	}
}
