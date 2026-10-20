package metrics

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// SeriesLimits 限制 Monitor 每小时、每天新建的指标序列数，防止上报方的标签失控把目录撑爆。
// 借鉴 VictoriaMetrics 的 -storage.maxHourlySeries / -storage.maxDailySeries：已有序列照常更新，只拦新序列。
// 任一值 ≤ 0 表示该窗口不限。
type SeriesLimits struct {
	MaxNewPerHour int
	MaxNewPerDay  int
}

func (l SeriesLimits) enabled() bool { return l.MaxNewPerHour > 0 || l.MaxNewPerDay > 0 }

// seriesLimitAlertRatio 是新序列数达到上限的这个比例时，健康检查就提示接近上限。
const seriesLimitAlertRatio = 0.9

// SeriesLimitStatus 是新序列限额的当前使用情况。
type SeriesLimitStatus struct {
	Enabled                bool
	HourCurrent, HourMax   int64
	DayCurrent, DayMax     int64
	Drops                  []SeriesDrop // 近 1 小时仍有丢弃的服务
	NearLimit, HasDropping bool
}

// Healthy 报告新序列数是否在限额内：没有丢弃，也没有接近上限。
func (s SeriesLimitStatus) Healthy() bool { return !s.NearLimit && !s.HasDropping }

func nearLimit(current, max int64) bool {
	return max > 0 && float64(current) >= float64(max)*seriesLimitAlertRatio
}

// SetSeriesLimits 设置新序列限额；零值关闭限制。
func (r *MetricMessageStore) SetSeriesLimits(limits SeriesLimits) {
	if r != nil {
		r.limits = limits
		publishSeriesLimit(limits)
	}
}

type droppedSeries struct {
	service, metric string
	count           int
}

// admitSamples 在入库事务里挑出允许写入的样本：已有序列一律放行，新序列在限额内放行，超出的丢弃。
// 返回放行的样本、被丢弃序列的统计，以及事务内看到的“近 1 小时、近 24 小时新增序列数”（含本次放行的）。
func (r *MetricMessageStore) admitSamples(tx *gorm.DB, samples []Sample, now time.Time) (admitted []Sample, dropped []droppedSeries, hour, day int64, err error) {
	limits := r.limits
	if !limits.enabled() {
		return samples, nil, 0, 0, nil
	}
	// 空样本也要读真实的窗口计数：gauge 反映的是目录里的实际新增数，不能因为一份空快照被写成 0。
	if err := tx.Model(&MetricSeries{}).Where("c_ctime >= ?", now.Add(-time.Hour)).Count(&hour).Error; err != nil {
		return nil, nil, 0, 0, err
	}
	if err := tx.Model(&MetricSeries{}).Where("c_ctime >= ?", now.Add(-24*time.Hour)).Count(&day).Error; err != nil {
		return nil, nil, 0, 0, err
	}
	if len(samples) == 0 {
		return samples, nil, hour, day, nil
	}
	existing, err := existingSeries(tx, samples)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	decided := make(map[seriesKey]bool, len(samples))
	admitted = make([]Sample, 0, len(samples))
	droppedCount := make(map[[2]string]int)
	for _, sample := range samples {
		key := seriesKey{sample.ServiceName, sample.InstanceID, sample.SeriesID}
		ok, seen := decided[key]
		if !seen {
			switch {
			case existing[key]:
				ok = true
			case (limits.MaxNewPerHour > 0 && hour >= int64(limits.MaxNewPerHour)) || (limits.MaxNewPerDay > 0 && day >= int64(limits.MaxNewPerDay)):
				ok = false
			default:
				ok = true
				hour++
				day++
			}
			decided[key] = ok
			if !ok {
				droppedCount[[2]string{sample.ServiceName, sample.MetricName}]++
			}
		}
		if ok {
			admitted = append(admitted, sample)
		}
	}
	for key, count := range droppedCount {
		dropped = append(dropped, droppedSeries{service: key[0], metric: key[1], count: count})
	}
	return admitted, dropped, hour, day, nil
}

type seriesKey struct{ service, instance, series string }

func existingSeries(tx *gorm.DB, samples []Sample) (map[seriesKey]bool, error) {
	byInstance := make(map[[2]string][]string)
	for _, sample := range samples {
		k := [2]string{sample.ServiceName, sample.InstanceID}
		byInstance[k] = append(byInstance[k], sample.SeriesID)
	}
	out := make(map[seriesKey]bool, len(samples))
	for instance, ids := range byInstance {
		for start := 0; start < len(ids); start += ingestBatchRows {
			end := min(start+ingestBatchRows, len(ids))
			var found []string
			if err := tx.Model(&MetricSeries{}).
				Where("c_service_name = ? AND c_instance_id = ? AND c_series_id IN ?", instance[0], instance[1], ids[start:end]).
				Pluck("c_series_id", &found).Error; err != nil {
				return nil, err
			}
			for _, id := range found {
				out[seriesKey{instance[0], instance[1], id}] = true
			}
		}
	}
	return out, nil
}

// SeriesLimitStatus 读出新序列限额的使用情况：数量来自目录表（重启后仍准确），丢弃情况来自进程内的诊断。
func (r *MetricMessageStore) SeriesLimitStatus(ctx context.Context, now time.Time) (SeriesLimitStatus, error) {
	if r == nil || r.db == nil {
		return SeriesLimitStatus{}, errors.New("metrics store is not initialized")
	}
	limits := r.limits
	status := SeriesLimitStatus{Enabled: limits.enabled(), HourMax: int64(limits.MaxNewPerHour), DayMax: int64(limits.MaxNewPerDay)}
	if !status.Enabled {
		return status, nil
	}
	db := r.db.WithContext(ctx)
	if err := db.Model(&MetricSeries{}).Where("c_ctime >= ?", now.Add(-time.Hour)).Count(&status.HourCurrent).Error; err != nil {
		return SeriesLimitStatus{}, err
	}
	if err := db.Model(&MetricSeries{}).Where("c_ctime >= ?", now.Add(-24*time.Hour)).Count(&status.DayCurrent).Error; err != nil {
		return SeriesLimitStatus{}, err
	}
	status.Drops = r.diag.drops(now.Add(-time.Hour))
	status.HasDropping = len(status.Drops) > 0
	status.NearLimit = nearLimit(status.HourCurrent, status.HourMax) || nearLimit(status.DayCurrent, status.DayMax)
	return status, nil
}

// IngestRejects 返回各服务最近被拒收的快照及原因。
func (r *MetricMessageStore) IngestRejects() []IngestReject {
	if r == nil {
		return nil
	}
	return r.diag.rejects()
}
