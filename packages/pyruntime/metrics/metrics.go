package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

type Metrics struct {
	Tasks    *prometheus.CounterVec
	Duration *prometheus.HistogramVec
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{Tasks: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_pyruntime_worker_task_total", Help: "Python runtime tasks."}, []string{"module_type", "encoding", "status"}), Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_pyruntime_worker_task_duration_seconds", Help: "Python runtime task duration."}, []string{"module_type", "status"})}
	reg.MustRegister(m.Tasks, m.Duration)
	return m
}
func (m *Metrics) Observe(module, encoding, status string, d time.Duration) {
	m.Tasks.WithLabelValues(module, encoding, status).Inc()
	m.Duration.WithLabelValues(module, status).Observe(d.Seconds())
}
