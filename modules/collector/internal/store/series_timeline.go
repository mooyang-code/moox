package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/seriesset"
	"gorm.io/gorm"
)

// SeriesTimelineEntry 是标的时间线的一行：一个采集数据集的一个频率从 EffectiveFrom（K 线开始时间，Unix 秒）起应有的序列。
// Series 为空表示从这一刻起不再采集。
type SeriesTimelineEntry struct {
	SpaceID       string
	DatasetID     string
	Frequency     string
	EffectiveFrom int64
	SeriesHash    string
	Series        []seriesset.Series
	LatenessS     int
	OutputFields  []string
}

type seriesTimelineRow struct {
	ID            int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string    `gorm:"column:c_space_id"`
	DatasetID     string    `gorm:"column:c_dataset_id"`
	Frequency     string    `gorm:"column:c_frequency"`
	EffectiveFrom int64     `gorm:"column:c_effective_from"`
	SeriesHash    string    `gorm:"column:c_series_hash"`
	Series        string    `gorm:"column:c_series"`
	LatenessS     int       `gorm:"column:c_lateness_s"`
	OutputFields  string    `gorm:"column:c_output_fields"`
	CreatedAt     time.Time `gorm:"column:c_ctime"`
}

func (seriesTimelineRow) TableName() string { return "t_collector_series_timeline" }

// SeriesTimelineRepository 保存标的时间线。行只由采集计划的对账写入（见 FetchPlanRepository 的 Create、Update、
// ReregisterAppliedPlan，都与计划记录在同一个事务里），检查、补采、历史扫描、收盘重取、健康度只读。
// 读取按写入先后重放；同一生效点后写的覆盖先写的；重新登记时整空间删除。
type SeriesTimelineRepository struct{ db *gorm.DB }

func NewSeriesTimelineRepository(db *gorm.DB) *SeriesTimelineRepository {
	return &SeriesTimelineRepository{db: db}
}

// ListBySpace 按写入先后返回该空间的全部时间线行。
func (r *SeriesTimelineRepository) ListBySpace(ctx context.Context, spaceID string) ([]SeriesTimelineEntry, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("series timeline repository is not initialized")
	}
	var rows []seriesTimelineRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", strings.TrimSpace(spaceID)).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	entries := make([]SeriesTimelineEntry, 0, len(rows))
	for _, row := range rows {
		entry := SeriesTimelineEntry{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, EffectiveFrom: row.EffectiveFrom, SeriesHash: row.SeriesHash, LatenessS: row.LatenessS}
		if err := json.Unmarshal([]byte(row.Series), &entry.Series); err != nil {
			return nil, fmt.Errorf("decode series timeline row %d: %w", row.ID, err)
		}
		if err := json.Unmarshal([]byte(row.OutputFields), &entry.OutputFields); err != nil {
			return nil, fmt.Errorf("decode series timeline row %d: %w", row.ID, err)
		}
		// 空列表统一读成 nil，与写入前的值一致。
		if len(entry.Series) == 0 {
			entry.Series = nil
		}
		if len(entry.OutputFields) == 0 {
			entry.OutputFields = nil
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// appendSeriesTimeline 在调用方的事务里追加时间线行。同一生效点的旧行先删再插：新行拿到新的 c_id，排在最后，
// 时间线按写入先后重放（见 klinecheck.BuildTimeline），后写的那一行说了算。
func appendSeriesTimeline(tx *gorm.DB, entries []SeriesTimelineEntry) error {
	for _, entry := range entries {
		row := seriesTimelineRow{
			SpaceID: strings.TrimSpace(entry.SpaceID), DatasetID: strings.TrimSpace(entry.DatasetID), Frequency: strings.TrimSpace(entry.Frequency),
			EffectiveFrom: entry.EffectiveFrom, SeriesHash: entry.SeriesHash, LatenessS: entry.LatenessS, CreatedAt: time.Now().UTC(),
		}
		if row.SpaceID == "" || row.DatasetID == "" || row.Frequency == "" || row.EffectiveFrom <= 0 {
			return fmt.Errorf("series timeline entry needs space_id, dataset_id, frequency and a positive effective_from")
		}
		series, outputFields := entry.Series, entry.OutputFields
		if series == nil {
			series = []seriesset.Series{}
		}
		if outputFields == nil {
			outputFields = []string{}
		}
		rawSeries, err := json.Marshal(series)
		if err != nil {
			return err
		}
		rawFields, err := json.Marshal(outputFields)
		if err != nil {
			return err
		}
		row.Series, row.OutputFields = string(rawSeries), string(rawFields)
		if err := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_effective_from = ?", row.SpaceID, row.DatasetID, row.Frequency, row.EffectiveFrom).Delete(&seriesTimelineRow{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func deleteSeriesTimeline(tx *gorm.DB, spaceID string) error {
	return tx.Where("c_space_id = ?", strings.TrimSpace(spaceID)).Delete(&seriesTimelineRow{}).Error
}
