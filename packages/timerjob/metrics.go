package timerjob

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	jobRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_timer_job_runs_total",
		Help: "Timer job invocations by bounded job name and result.",
	}, []string{"job", "result"})
	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moox_timer_job_duration_seconds",
		Help:    "Timer job execution duration by bounded job name.",
		Buckets: prometheus.DefBuckets,
	}, []string{"job"})
)

func init() {
	prometheus.MustRegister(jobRuns, jobDuration)
}

func observe(name string, result Result, duration time.Duration) {
	jobRuns.WithLabelValues(name, string(result)).Inc()
	jobDuration.WithLabelValues(name).Observe(duration.Seconds())
}
