package observability

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DeadlineRejections 导出每个数据集最近一次“周期建立前截止已过”拒绝的时刻（moox_storage_period_deadline_rejected_timestamp_seconds）。
// 写入方取数晚于数据集截止时整期提交都会被拒，Monitor 据此告警；序列数以数据集为界，超过上限的不再新建。
type DeadlineRejections struct {
	gauge    *prometheus.GaugeVec
	maxItems int

	mu    sync.Mutex
	known map[[3]string]struct{}
}

func NewDeadlineRejections(registerer prometheus.Registerer) (*DeadlineRejections, error) {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "moox_storage_period_deadline_rejected_timestamp_seconds",
		Help: "Unix timestamp of the last period commit rejected because the dataset deadline passed before the period was created.",
	}, []string{"space_id", "dataset_id", "freq"})
	if err := registerer.Register(gauge); err != nil {
		return nil, err
	}
	return &DeadlineRejections{gauge: gauge, maxItems: MaxDatasetSeries, known: map[[3]string]struct{}{}}, nil
}

func (d *DeadlineRejections) ObserveDeadlineRejection(spaceID, datasetID, frequency string, at time.Time) {
	if d == nil || spaceID == "" || datasetID == "" {
		return
	}
	key := [3]string{spaceID, datasetID, frequency}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.known[key]; !ok {
		if len(d.known) >= d.maxItems {
			return
		}
		d.known[key] = struct{}{}
	}
	d.gauge.WithLabelValues(spaceID, datasetID, frequency).Set(float64(at.UTC().Unix()))
}
