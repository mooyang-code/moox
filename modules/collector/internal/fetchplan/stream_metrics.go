package fetchplan

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// StreamMetrics 是 Collector 汇总的推送采集指标（由 collector-stream 心跳与周期统计生成）。心跳中断由关注项 stream_heartbeat_lost 判断，不在这里。
type StreamMetrics struct {
	arrivals    *prometheus.CounterVec
	missed      *prometheus.CounterVec
	suspected   *prometheus.CounterVec
	commitDone  *prometheus.HistogramVec
	reconnects  *prometheus.GaugeVec
	periods     *prometheus.CounterVec
	clockOffset *prometheus.GaugeVec
}

// NewStreamMetrics 注册推送采集指标。
func NewStreamMetrics(reg prometheus.Registerer) *StreamMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &StreamMetrics{
		arrivals:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_final_arrivals_total", Help: "推送到达的收盘定稿数。"}, []string{"space_id", "market", "frequency"}),
		missed:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_missed_total", Help: "截止时仍缺的标的数（不含疑似下架）。"}, []string{"space_id", "market", "frequency"}),
		suspected:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_suspected_total", Help: "截止时仍缺且已判定疑似下架的标的数。"}, []string{"space_id", "market", "frequency"}),
		commitDone:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_collector_stream_commit_done_seconds", Help: "周期提交完成相对收盘的秒数。", Buckets: []float64{0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8}}, []string{"space_id", "market", "frequency"}),
		reconnects:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_reconnects_total", Help: "collector-stream 各连接累计重连次数（实例重启后归零）。"}, []string{"space_id", "market", "frequency"}),
		periods:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_periods_total", Help: "推送周期数（按结果：complete、degraded、zero、failed）。"}, []string{"space_id", "market", "frequency", "result"}),
		clockOffset: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_clock_offset_seconds", Help: "collector-stream 时钟减 Collector 时钟的估计值。"}, []string{"space_id"}),
	}
	m.arrivals = registerCounterVec(reg, m.arrivals)
	m.missed = registerCounterVec(reg, m.missed)
	m.suspected = registerCounterVec(reg, m.suspected)
	if err := reg.Register(m.commitDone); err != nil {
		if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
			m.commitDone = existing.ExistingCollector.(*prometheus.HistogramVec)
		}
	}
	m.reconnects = registerGaugeVec(reg, m.reconnects)
	m.periods = registerCounterVec(reg, m.periods)
	m.clockOffset = registerGaugeVec(reg, m.clockOffset)
	return m
}

// ObserveHeartbeat 记录一次心跳。
func (m *StreamMetrics) ObserveHeartbeat(spaceID string, at time.Time, clockOffset time.Duration, reconnects map[[2]string]int64) {
	if m == nil {
		return
	}
	m.clockOffset.WithLabelValues(spaceID).Set(clockOffset.Seconds())
	for key, value := range reconnects {
		m.reconnects.WithLabelValues(spaceID, key[0], key[1]).Set(float64(value))
	}
}

// StreamPeriodObservation 是一期统计里 Collector 关心的部分。
type StreamPeriodObservation struct {
	Market, DatasetID, Frequency          string
	Expected, Arrived, Missing, Suspected int
	Committed                             bool
	CommitDone                            time.Duration
}

// ObservePeriod 记录一期统计。
func (m *StreamMetrics) ObservePeriod(spaceID string, period StreamPeriodObservation) {
	if m == nil {
		return
	}
	labels := []string{spaceID, period.Market, period.Frequency}
	m.arrivals.WithLabelValues(labels...).Add(float64(period.Arrived))
	failed := max(period.Missing-period.Suspected, 0)
	m.missed.WithLabelValues(labels...).Add(float64(failed))
	m.suspected.WithLabelValues(labels...).Add(float64(period.Suspected))
	result := "complete"
	switch {
	case period.Arrived == 0:
		result = "zero"
	case !period.Committed:
		result = "failed"
	case period.Missing > 0:
		result = "degraded"
	}
	m.periods.WithLabelValues(spaceID, period.Market, period.Frequency, result).Inc()
	if period.Committed {
		m.commitDone.WithLabelValues(labels...).Observe(period.CommitDone.Seconds())
	}
}
