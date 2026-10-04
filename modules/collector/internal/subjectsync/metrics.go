package subjectsync

import (
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 上报标签同步与属性维护的运行状态。成员数来自 Storage 的实时统计。
type Metrics struct {
	lastSuccess *prometheus.GaugeVec
	active      *prometheus.GaugeVec
	inactive    *prometheus.GaugeVec
	failures    *prometheus.CounterVec
	attrErrors  *prometheus.CounterVec
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_subject_tag_last_success_timestamp", Help: "Unix time of the last successful subject tag run."}, []string{"space", "tag"}),
		active:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_subject_tag_active_members", Help: "Active members of a subject tag."}, []string{"space", "tag"}),
		inactive:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_subject_tag_inactive_members", Help: "Inactive members of a subject tag."}, []string{"space", "tag"}),
		failures:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_subject_tag_failures_total", Help: "Failed subject tag runs."}, []string{"space", "tag"}),
		attrErrors:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_subject_attribute_failures_total", Help: "Failed subject attribute maintenance runs."}, []string{"space"}),
	}
	for _, collector := range []prometheus.Collector{m.lastSuccess, m.active, m.inactive, m.failures, m.attrErrors} {
		_ = registerer.Register(collector)
	}
	return m
}

func (m *Metrics) observeTagInventory(tag *pb.Tag) {
	if m == nil || tag == nil {
		return
	}
	labels := []string{tag.GetSpaceId(), tag.GetTagId()}
	m.active.WithLabelValues(labels...).Set(float64(tag.GetActiveCount()))
	m.inactive.WithLabelValues(labels...).Set(float64(tag.GetInactiveCount()))
	if tag.GetLastStatus() != "success" || tag.GetLastRunAt() == "" {
		return
	}
	lastRun, err := time.ParseInLocation(sqliteTimeLayout, tag.GetLastRunAt(), time.UTC)
	if err != nil {
		return
	}
	m.lastSuccess.WithLabelValues(labels...).Set(float64(lastRun.Unix()))
}

func (m *Metrics) observeTagFailure(tag *pb.Tag) {
	if m == nil || tag == nil {
		return
	}
	m.failures.WithLabelValues(tag.GetSpaceId(), tag.GetTagId()).Inc()
}

func (m *Metrics) observeTagSuccess(tag *pb.Tag, runAt time.Time) {
	if m == nil || tag == nil {
		return
	}
	m.lastSuccess.WithLabelValues(tag.GetSpaceId(), tag.GetTagId()).Set(float64(runAt.UTC().Unix()))
}

func (m *Metrics) observeAttributeFailure(spaceID string) {
	if m == nil {
		return
	}
	m.attrErrors.WithLabelValues(spaceID).Inc()
}
