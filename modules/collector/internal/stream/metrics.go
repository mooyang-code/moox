package stream

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 是 collector-stream 的本机指标（/metrics）；Collector 侧的汇总指标由 ReportStreamPeriods 上报后生成。
type Metrics struct {
	arrival     *prometheus.HistogramVec
	commitDone  *prometheus.HistogramVec
	commitTook  *prometheus.HistogramVec
	periods     *prometheus.CounterVec
	missing     *prometheus.CounterVec
	late        *prometheus.CounterVec
	stale       *prometheus.CounterVec
	reconnects  *prometheus.CounterVec
	streams     *prometheus.GaugeVec
	lastMessage *prometheus.GaugeVec
}

// NewMetrics 注册指标；registerer 为空时用默认注册表。
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	buckets := []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 15}
	m := &Metrics{
		arrival:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_stream_final_arrival_seconds", Help: "收盘定稿推送到达本机相对收盘的秒数。", Buckets: buckets}, []string{"market", "frequency"}),
		commitDone:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_stream_commit_done_seconds", Help: "周期提交完成相对收盘的秒数。", Buckets: buckets}, []string{"market", "frequency"}),
		commitTook:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_stream_commit_duration_seconds", Help: "一次周期提交的耗时。", Buckets: buckets}, []string{"market", "frequency"}),
		periods:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_stream_periods_total", Help: "结束的周期数。"}, []string{"market", "frequency", "result"}),
		missing:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_stream_period_missing_total", Help: "截止时仍缺的标的数累计。"}, []string{"market", "frequency"}),
		late:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_stream_late_after_commit_total", Help: "提交之后才到的定稿数。"}, []string{"market", "frequency"}),
		stale:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_stream_stale_finals_total", Help: "过期而被丢弃的定稿数。"}, []string{"market"}),
		reconnects:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_stream_reconnects_total", Help: "WS 重连次数。"}, []string{"market", "group"}),
		streams:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_stream_subscribed_streams", Help: "期望订阅的流数。"}, []string{"market", "group"}),
		lastMessage: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_stream_last_message_timestamp", Help: "最近一条消息的 Unix 时间。"}, []string{"market", "group"}),
	}
	for _, collector := range []prometheus.Collector{m.arrival, m.commitDone, m.commitTook, m.periods, m.missing, m.late, m.stale, m.reconnects, m.streams, m.lastMessage} {
		_ = registerer.Register(collector)
	}
	return m
}

func (m *Metrics) observeArrival(market, frequency string, since time.Duration) {
	if m != nil {
		m.arrival.WithLabelValues(market, frequency).Observe(since.Seconds())
	}
}

func (m *Metrics) observeCommit(market, frequency string, done, took time.Duration) {
	if m != nil {
		m.commitDone.WithLabelValues(market, frequency).Observe(done.Seconds())
		m.commitTook.WithLabelValues(market, frequency).Observe(took.Seconds())
	}
}

func (m *Metrics) observeStale(market string) {
	if m != nil {
		m.stale.WithLabelValues(market).Inc()
	}
}

func (m *Metrics) observePeriod(stat PeriodStat) {
	if m == nil {
		return
	}
	result := "complete"
	switch {
	case stat.Arrived == 0:
		result = "zero"
	case !stat.Committed:
		result = "failed"
	case stat.Missing > 0:
		result = "degraded"
	}
	m.periods.WithLabelValues(stat.Market, stat.Frequency, result).Inc()
	m.missing.WithLabelValues(stat.Market, stat.Frequency).Add(float64(stat.Missing))
	m.late.WithLabelValues(stat.Market, stat.Frequency).Add(float64(stat.LateAfterCommit))
}

func (m *Metrics) observeConns(market string, status []ConnStatus) {
	if m == nil {
		return
	}
	for _, conn := range status {
		m.streams.WithLabelValues(market, conn.Group).Set(float64(conn.Streams))
		if !conn.LastMessage.IsZero() {
			m.lastMessage.WithLabelValues(market, conn.Group).Set(float64(conn.LastMessage.Unix()))
		}
	}
}

func (m *Metrics) observeReconnect(market, group string) {
	if m != nil {
		m.reconnects.WithLabelValues(market, group).Inc()
	}
}
