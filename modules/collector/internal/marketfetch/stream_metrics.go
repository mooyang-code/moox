package marketfetch

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// StreamMetrics 是 Collector 汇总的推送采集指标（由 collector-stream 心跳与周期统计生成），Monitor 据此告警。
type StreamMetrics struct {
	online       *prometheus.GaugeVec
	heartbeatAt  *prometheus.GaugeVec
	arrivals     *prometheus.CounterVec
	missed       *prometheus.CounterVec
	suspected    *prometheus.CounterVec
	commitDone   *prometheus.HistogramVec
	reconnects   *prometheus.GaugeVec
	periods      *prometheus.CounterVec
	failedStreak *prometheus.GaugeVec
	clockOffset  *prometheus.GaugeVec
}

// NewStreamMetrics 注册推送采集指标。
func NewStreamMetrics(reg prometheus.Registerer) *StreamMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &StreamMetrics{
		online:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_online", Help: "collector-stream 心跳是否在超时之内（1 在线）。"}, []string{"space_id"}),
		heartbeatAt:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_heartbeat_timestamp_seconds", Help: "collector-stream 最近一次心跳的 Unix 时间。"}, []string{"space_id"}),
		arrivals:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_final_arrivals_total", Help: "推送到达的收盘定稿数。"}, []string{"space_id", "market", "frequency"}),
		missed:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_missed_total", Help: "截止时仍缺的标的数（不含疑似下架）。"}, []string{"space_id", "market", "frequency"}),
		suspected:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_period_suspected_total", Help: "截止时仍缺且已判定疑似下架的标的数。"}, []string{"space_id", "market", "frequency"}),
		commitDone:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_collector_stream_commit_done_seconds", Help: "周期提交完成相对收盘的秒数。", Buckets: []float64{0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8}}, []string{"space_id", "market", "frequency"}),
		reconnects:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_reconnects_total", Help: "collector-stream 各连接累计重连次数（实例重启后归零）。"}, []string{"space_id", "market", "frequency"}),
		periods:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_stream_periods_total", Help: "推送周期数（按结果：complete、degraded、zero、failed）。"}, []string{"space_id", "market", "frequency", "result"}),
		failedStreak: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_failed_period_streak", Help: "连续有失败标的（不含疑似下架）的周期数。"}, []string{"space_id", "market", "frequency", "dataset_id"}),
		clockOffset:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_stream_clock_offset_seconds", Help: "collector-stream 时钟减 Collector 时钟的估计值。"}, []string{"space_id"}),
	}
	m.online = registerGaugeVec(reg, m.online)
	m.heartbeatAt = registerGaugeVec(reg, m.heartbeatAt)
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
	m.failedStreak = registerGaugeVec(reg, m.failedStreak)
	m.clockOffset = registerGaugeVec(reg, m.clockOffset)
	return m
}

// ObserveHeartbeat 记录一次心跳。
func (m *StreamMetrics) ObserveHeartbeat(spaceID string, at time.Time, clockOffset time.Duration, reconnects map[[2]string]int64) {
	if m == nil {
		return
	}
	m.heartbeatAt.WithLabelValues(spaceID).Set(float64(at.Unix()))
	m.online.WithLabelValues(spaceID).Set(1)
	m.clockOffset.WithLabelValues(spaceID).Set(clockOffset.Seconds())
	for key, value := range reconnects {
		m.reconnects.WithLabelValues(spaceID, key[0], key[1]).Set(float64(value))
	}
}

// ObserveOnline 按心跳超时刷新在线状态。
func (m *StreamMetrics) ObserveOnline(spaceID string, online bool) {
	if m == nil {
		return
	}
	value := 0.0
	if online {
		value = 1
	}
	m.online.WithLabelValues(spaceID).Set(value)
}

// StreamPeriodObservation 是一期统计里 Collector 关心的部分。
type StreamPeriodObservation struct {
	Market, DatasetID, Frequency          string
	Expected, Arrived, Missing, Suspected int
	Committed                             bool
	CommitDone                            time.Duration
	FailedStreak                          int
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
	m.failedStreak.WithLabelValues(spaceID, period.Market, period.Frequency, period.DatasetID).Set(float64(period.FailedStreak))
}
