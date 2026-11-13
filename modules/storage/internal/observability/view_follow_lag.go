package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// ViewFollowLag 是一个 View 跟随变更日志的落后情况。
type ViewFollowLag struct {
	SpaceID string
	ViewID  string
	// Changes 是日志头部序号减去已扫描序号（空闲数据集也会有不超过一轮等待的差值）。
	Changes uint64
	// Seconds 是距离上一次追上日志头部的时长；正在等待新变更时为 0。
	Seconds float64
}

// viewFollowLagCollector 在采集时向跟随器要当前的落后情况：卡住的跟随器不会自己更新指标，
// 采集时现算才能反映它越落越远；已删除的 View 没有跟随器，指标随之消失。
type viewFollowLagCollector struct {
	source     atomic.Pointer[func() []ViewFollowLag]
	changeDesc *prometheus.Desc
	secondDesc *prometheus.Desc
}

func newViewFollowLagCollector() *viewFollowLagCollector {
	return &viewFollowLagCollector{
		changeDesc: prometheus.NewDesc("moox_storage_view_follow_lag_changes",
			"Change log entries between the head and the position a View has scanned.", []string{"space_id", "view_id"}, nil),
		secondDesc: prometheus.NewDesc("moox_storage_view_follow_lag_seconds",
			"Seconds since a View last caught up with the change log head; 0 while waiting for new changes.", []string{"space_id", "view_id"}, nil),
	}
}

func (c *viewFollowLagCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.changeDesc
	ch <- c.secondDesc
}

func (c *viewFollowLagCollector) Collect(ch chan<- prometheus.Metric) {
	source := c.source.Load()
	if source == nil || *source == nil {
		return
	}
	for _, lag := range (*source)() {
		ch <- prometheus.MustNewConstMetric(c.changeDesc, prometheus.GaugeValue, float64(lag.Changes), lag.SpaceID, lag.ViewID)
		ch <- prometheus.MustNewConstMetric(c.secondDesc, prometheus.GaugeValue, lag.Seconds, lag.SpaceID, lag.ViewID)
	}
}

// SetFollowLagSource 设置跟随器落后情况的来源；nil 表示不再上报。
func (m *ViewMetrics) SetFollowLagSource(source func() []ViewFollowLag) {
	if m == nil || m.followLag == nil {
		return
	}
	if source == nil {
		m.followLag.source.Store(nil)
		return
	}
	m.followLag.source.Store(&source)
}
