package report

import "github.com/prometheus/client_golang/prometheus"

// ArchiveTaskHealthMetric 是 archive-mgr 上报的归档任务健康等级，每个任务一条序列（标签 space、dataset），
// Monitor 据此生成“归档任务”检查，并用它判断哪些数据集还没有归档任务。
const ArchiveTaskHealthMetric = "moox_archive_task_health"

// 归档任务健康等级的取值。
const (
	ArchiveTaskHealthOK      = 0
	ArchiveTaskHealthWarning = 1
	ArchiveTaskHealthError   = 2
	ArchiveTaskHealthPaused  = 3
)

// ArchiveTaskHealth 是归档任务健康等级的指标。
type ArchiveTaskHealth struct {
	gauge *prometheus.GaugeVec
}

// NewArchiveTaskHealth 注册指标。
func NewArchiveTaskHealth(registerer prometheus.Registerer) (*ArchiveTaskHealth, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	h := &ArchiveTaskHealth{gauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: ArchiveTaskHealthMetric, Help: "归档任务健康等级：0 正常，1 需关注，2 异常，3 已停用。",
	}, []string{"space", "dataset"})}
	if err := registerer.Register(h.gauge); err != nil {
		return nil, err
	}
	return h, nil
}

// ArchiveTaskLevel 是一个任务的健康等级。
type ArchiveTaskLevel struct {
	Space, Dataset string
	Level          int
}

// Set 用当前全部任务替换指标：已删除的任务随之消失。
func (h *ArchiveTaskHealth) Set(levels []ArchiveTaskLevel) {
	h.gauge.Reset()
	for _, l := range levels {
		h.gauge.WithLabelValues(l.Space, l.Dataset).Set(float64(l.Level))
	}
}
