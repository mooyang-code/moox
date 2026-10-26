package metrics

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BarArrivalRetention 是逐根到达记录的保留时长，按到达时间清理：周线、月线到达时开盘早已超过 7 天，按开盘清理会刚写入就删掉。
const BarArrivalRetention = 7 * 24 * time.Hour

const (
	viewOutputWatermarkMetric = "moox_storage_view_output_watermark_timestamp_seconds"
	viewOutputArrivalMetric   = "moox_storage_view_output_arrival_timestamp_seconds"
	datasetOutputWatermark    = "_dataset_output_watermark_timestamp_seconds"
	datasetOutputArrival      = "_dataset_output_arrival_timestamp_seconds"
)

// barArrivalProducers 是记录到达的生产者，与数据链路的环节一致：入库、视图、因子、策略计算。
var barArrivalProducers = map[string]struct{}{"storage": {}, "storage_view": {}, "factor": {}, "strategy": {}}

// BarArrival 是一次输出水位前进：生产者在 ArrivedAt 写完了开盘时间为 BarAt 的那一根。PreviousBarAt 是上一份快照里的水位，
// 两者之间若隔了几根（补数据时一批写完），那几根不单独记录。DatasetID 对 View 是 view_id，PrimaryDatasetID 是它服务的数据集。
type BarArrival struct {
	ID               uint64     `gorm:"column:c_id;primaryKey"`
	Producer         string     `gorm:"column:c_producer"`
	SpaceID          string     `gorm:"column:c_space_id"`
	DatasetID        string     `gorm:"column:c_dataset_id"`
	PrimaryDatasetID string     `gorm:"column:c_primary_dataset_id"`
	Freq             string     `gorm:"column:c_freq"`
	BarAt            time.Time  `gorm:"column:c_bar_at"`
	PreviousBarAt    *time.Time `gorm:"column:c_previous_bar_at"`
	ArrivedAt        time.Time  `gorm:"column:c_arrived_at"`
	ReportedAt       time.Time  `gorm:"column:c_reported_at"`
	ServiceName      string     `gorm:"column:c_service_name"`
	InstanceID       string     `gorm:"column:c_instance_id"`
}

func (BarArrival) TableName() string { return "t_monitor_bar_arrivals" }

// outputArrivalMetric 判断一个指标是不是输出水位；是的话返回生产者和同一身份的到达时间指标名。
func outputArrivalMetric(metricName string) (producer, arrivalMetric string, ok bool) {
	if metricName == viewOutputWatermarkMetric {
		return "storage_view", viewOutputArrivalMetric, true
	}
	if !strings.HasPrefix(metricName, "moox_") || !strings.HasSuffix(metricName, datasetOutputWatermark) {
		return "", "", false
	}
	module := strings.TrimSuffix(strings.TrimPrefix(metricName, "moox_"), datasetOutputWatermark)
	return module, "moox_" + module + datasetOutputArrival, module != ""
}

// detectBarArrivals 从一份快照里找出前进了的输出水位：previous 是入库前各水位序列已提交的值（没有记录表示第一次见到）。
// 没有交易日历的系统数据（mooxsys）不记录；缺到达时间的不记录，不推算。
func detectBarArrivals(samples []Sample, previous map[string]float64) []BarArrival {
	type arrivalKey struct{ service, instance, metric, labels string }
	arrivals := make(map[arrivalKey]float64)
	for _, sample := range samples {
		if sample.MetricName == viewOutputArrivalMetric || strings.HasSuffix(sample.MetricName, datasetOutputArrival) {
			arrivals[arrivalKey{sample.ServiceName, sample.InstanceID, sample.MetricName, sample.LabelsJSON}] = sample.Value
		}
	}
	var out []BarArrival
	for _, sample := range samples {
		producer, arrivalMetric, ok := outputArrivalMetric(sample.MetricName)
		if !ok {
			continue
		}
		if _, tracked := barArrivalProducers[producer]; !tracked {
			continue
		}
		spaceID, freq := sample.Labels["space_id"], sample.Labels["freq"]
		datasetID, primaryDatasetID := sample.Labels["dataset_id"], ""
		if producer == "storage_view" {
			datasetID, primaryDatasetID = sample.Labels["view_id"], sample.Labels["dataset_id"]
		}
		if spaceID == "" || spaceID == InternalMetricSpaceID || datasetID == "" || freq == "" {
			continue
		}
		before, seen := previous[sample.SeriesID]
		if seen && sample.Value <= before {
			continue
		}
		barAt, ok := unixSecondsTime(sample.Value)
		if !ok {
			continue
		}
		arrivedAt, ok := unixSecondsTime(arrivals[arrivalKey{sample.ServiceName, sample.InstanceID, arrivalMetric, sample.LabelsJSON}])
		if !ok {
			continue
		}
		record := BarArrival{
			Producer: producer, SpaceID: spaceID, DatasetID: datasetID, PrimaryDatasetID: primaryDatasetID, Freq: freq,
			BarAt: barAt, ArrivedAt: arrivedAt, ReportedAt: sample.ObservedAt.UTC(),
			ServiceName: sample.ServiceName, InstanceID: sample.InstanceID,
		}
		if previousAt, ok := unixSecondsTime(before); seen && ok {
			record.PreviousBarAt = &previousAt
		}
		out = append(out, record)
	}
	return out
}

func unixSecondsTime(value float64) (time.Time, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 4102444800 {
		return time.Time{}, false
	}
	return time.Unix(int64(value), 0).UTC(), true
}

// recordBarArrivals 在指标入库的事务里记下水位前进：先读这些水位序列已提交的值，再写入新的到达记录。
// 与消息去重同一事务，重复投递不会重复记录，入库失败重试时也不会漏记；同一根已有记录时保留最早那条。
func recordBarArrivals(tx *gorm.DB, samples []Sample) error {
	var seriesIDs []string
	for _, sample := range samples {
		if _, _, ok := outputArrivalMetric(sample.MetricName); ok {
			seriesIDs = append(seriesIDs, sample.SeriesID)
		}
	}
	if len(seriesIDs) == 0 {
		return nil
	}
	var rows []MetricLatest
	if err := tx.Select("c_series_id", "c_value").Where("c_series_id IN ?", seriesIDs).Find(&rows).Error; err != nil {
		return err
	}
	previous := make(map[string]float64, len(rows))
	for _, row := range rows {
		previous[row.SeriesID] = row.Value
	}
	records := detectBarArrivals(samples, previous)
	if len(records) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&records, ingestBatchRows).Error
}

// BarArrivalChain 选出一条数据链路上的到达记录：入库按源数据集，因子按因子结果数据集（FactorDatasetID 为空表示没有选定任务，
// 不含因子环节），View 按主数据集是两者之一，策略的输入 Monitor 拿不到，取同空间同周期的全部。
type BarArrivalChain struct {
	SpaceID, Freq, SourceDatasetID, FactorDatasetID string
}

// ListBarArrivals 读取一条链路在 [from, to) 内开盘的到达记录，按开盘时间升序；超过上限时 truncated 为 true，结果只含前面的部分。
func (r *MetricMessageStore) ListBarArrivals(ctx context.Context, chain BarArrivalChain, from, to time.Time) (rows []BarArrival, truncated bool, err error) {
	if r == nil || r.db == nil {
		return nil, false, ErrMetricsStoreUnavailable
	}
	primaries := []string{chain.SourceDatasetID}
	members := r.db.Where("c_producer = 'storage' AND c_dataset_id = ?", chain.SourceDatasetID)
	if chain.FactorDatasetID != "" {
		primaries = append(primaries, chain.FactorDatasetID)
		members = members.Or("c_producer = 'factor' AND c_dataset_id = ?", chain.FactorDatasetID)
	}
	members = members.Or("c_producer = 'storage_view' AND c_primary_dataset_id IN ?", primaries).
		Or("c_producer = 'strategy'")
	err = r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_freq = ? AND c_bar_at >= ? AND c_bar_at < ?", chain.SpaceID, chain.Freq, from.UTC(), to.UTC()).
		Where(members).
		Order("c_bar_at ASC, c_id ASC").Limit(maxBarArrivalRows + 1).Find(&rows).Error
	if err != nil {
		return nil, false, err
	}
	if len(rows) > maxBarArrivalRows {
		return rows[:maxBarArrivalRows], true, nil
	}
	return rows, false, nil
}

// maxBarArrivalRows 是一次查询的上限：一条链路约五六个数据集，分钟线一天 1440 根，查询多带一天。测试可调小。
var maxBarArrivalRows = 50000

// PruneBarArrivals 删除到达时间早于 cutoff 的记录。
func (r *MetricMessageStore) PruneBarArrivals(ctx context.Context, cutoff time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("metrics store is not initialized")
	}
	result := r.db.WithContext(ctx).Where("c_arrived_at < ?", cutoff.UTC()).Delete(&BarArrival{})
	return result.RowsAffected, result.Error
}
