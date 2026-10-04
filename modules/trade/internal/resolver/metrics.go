package resolver

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics exposes resolver health without putting resolver internals into the
// RPC payload. It is optional so unit tests and disabled local Trade installs
// do not need a Prometheus registry.
type Metrics struct {
	Requests       prometheus.Counter
	Failures       prometheus.Counter
	Unresolved     prometheus.Counter
	ProbeFailures  prometheus.Counter
	LookupDuration prometheus.Histogram
	ProbeDuration  prometheus.Histogram
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Requests: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_trade_dns_resolver_requests_total",
			Help: "DNS resolver RPC requests received by Trade.",
		}),
		Failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_trade_dns_resolver_failures_total",
			Help: "DNS resolver request-level failures.",
		}),
		Unresolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_trade_dns_resolver_unresolved_domains_total",
			Help: "Configured domains that could not produce a usable IP.",
		}),
		ProbeFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "moox_trade_dns_resolver_probe_failures_total",
			Help: "Candidate IP TCP probe failures.",
		}),
		LookupDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "moox_trade_dns_resolver_lookup_duration_seconds",
			Help: "DNS lookup duration at the Trade node.",
		}),
		ProbeDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "moox_trade_dns_resolver_probe_duration_seconds",
			Help: "TCP probe batch duration at the Trade node.",
		}),
	}
	collectors := []prometheus.Collector{
		m.Requests, m.Failures, m.Unresolved, m.ProbeFailures,
		m.LookupDuration, m.ProbeDuration,
	}
	for _, collector := range collectors {
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
