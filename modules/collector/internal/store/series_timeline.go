package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/seriesset"
	"gorm.io/gorm"
)

// SeriesTimelineEntry 是标的时间线的一行：一个采集数据集的一个频率从 EffectiveFrom（K 线开始时间，Unix 秒）起应有的序列。
// Series 为空表示从这一刻起不再采集。Symbols 是这一段每个标的的交易所符号（标的 → 符号），补采按它取改名前的旧符号；
// 它不进序列哈希（哈希要与 Storage 登记的一致）。
type SeriesTimelineEntry struct {
	SpaceID       string
	DatasetID     string
	Frequency     string
	EffectiveFrom int64
	SeriesHash    string
	Series        []seriesset.Series
	Symbols       map[string]string
	LatenessS     int
	OutputFields  []string
}

type seriesTimelineRow struct {
	ID            int64  `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string `gorm:"column:c_space_id"`
	DatasetID     string `gorm:"column:c_dataset_id"`
	Frequency     string `gorm:"column:c_frequency"`
	EffectiveFrom int64  `gorm:"column:c_effective_from"`
	SeriesHash    string `gorm:"column:c_series_hash"`
	Series        string `gorm:"column:c_series"`
	Symbols       string `gorm:"column:c_external_symbols"`
	LatenessS     int    `gorm:"column:c_lateness_s"`
	OutputFields  string `gorm:"column:c_output_fields"`
	CreatedAt     int64  `gorm:"column:c_ctime"`
}

func (seriesTimelineRow) TableName() string { return "t_collector_series_timeline" }

// SeriesTimelineRepository 保存标的时间线。行只由采集计划的对账写入（见 FetchPlanRepository 的 Create、Update、
// ReregisterAppliedPlan，都与计划记录在同一个事务里），检查、补采、收盘重取、健康度只读。
// 读取按写入先后重放；同一生效点后写的覆盖先写的；重新登记时整空间删除。
type SeriesTimelineRepository struct{ db *gorm.DB }

func NewSeriesTimelineRepository(db *gorm.DB) *SeriesTimelineRepository {
	return &SeriesTimelineRepository{db: db}
}

// ListBySpace 按写入先后返回该空间的全部时间线行。
func (r *SeriesTimelineRepository) ListBySpace(ctx context.Context, spaceID string) ([]SeriesTimelineEntry, error) {
	entries, _, _, err := r.ListBySpaceWithBounds(ctx, spaceID)
	return entries, err
}

// ListBySpaceWithBounds 按写入先后返回该空间的全部时间线行及其首尾 c_id（空表时首尾均为 0）。
func (r *SeriesTimelineRepository) ListBySpaceWithBounds(ctx context.Context, spaceID string) (entries []SeriesTimelineEntry, minID, maxID int64, err error) {
	if r == nil || r.db == nil {
		return nil, 0, 0, fmt.Errorf("series timeline repository is not initialized")
	}
	var rows []seriesTimelineRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", strings.TrimSpace(spaceID)).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, 0, 0, err
	}
	entries, err = decodeSeriesTimelineRows(rows)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(rows) > 0 {
		minID, maxID = rows[0].ID, rows[len(rows)-1].ID
	}
	return entries, minID, maxID, nil
}

// TimelineStats 轻量查询空间时间线表的行数与首尾自增 ID，不读取大 JSON 列，用于内存缓存命中与删除检测。
func (r *SeriesTimelineRepository) TimelineStats(ctx context.Context, spaceID string) (count, minID, maxID int64, err error) {
	if r == nil || r.db == nil {
		return 0, 0, 0, fmt.Errorf("series timeline repository is not initialized")
	}
	var stats struct {
		Count int64 `gorm:"column:row_count"`
		MinID int64 `gorm:"column:min_id"`
		MaxID int64 `gorm:"column:max_id"`
	}
	err = r.db.WithContext(ctx).
		Raw(`SELECT COUNT(*) AS row_count, COALESCE(MIN(c_id), 0) AS min_id, COALESCE(MAX(c_id), 0) AS max_id FROM t_collector_series_timeline WHERE c_space_id = ?`, strings.TrimSpace(spaceID)).
		Scan(&stats).Error
	if err != nil {
		return 0, 0, 0, err
	}
	return stats.Count, stats.MinID, stats.MaxID, nil
}

// ListBySpaceAfter 按写入先后返回该空间 c_id 大于 afterID 的增量时间线行以及最后一条行的 c_id。
func (r *SeriesTimelineRepository) ListBySpaceAfter(ctx context.Context, spaceID string, afterID int64) (entries []SeriesTimelineEntry, lastID int64, err error) {
	if r == nil || r.db == nil {
		return nil, 0, fmt.Errorf("series timeline repository is not initialized")
	}
	var rows []seriesTimelineRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_id > ?", strings.TrimSpace(spaceID), afterID).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	entries, err = decodeSeriesTimelineRows(rows)
	if err != nil {
		return nil, 0, err
	}
	if len(rows) > 0 {
		lastID = rows[len(rows)-1].ID
	}
	return entries, lastID, nil
}

func decodeSeriesTimelineRows(rows []seriesTimelineRow) ([]SeriesTimelineEntry, error) {
	entries := make([]SeriesTimelineEntry, 0, len(rows))
	for _, row := range rows {
		entry := SeriesTimelineEntry{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, EffectiveFrom: row.EffectiveFrom, SeriesHash: row.SeriesHash, LatenessS: row.LatenessS}
		if err := json.Unmarshal([]byte(row.Series), &entry.Series); err != nil {
			return nil, fmt.Errorf("decode series timeline row %d: %w", row.ID, err)
		}
		if err := json.Unmarshal([]byte(row.OutputFields), &entry.OutputFields); err != nil {
			return nil, fmt.Errorf("decode series timeline row %d: %w", row.ID, err)
		}
		if err := json.Unmarshal([]byte(row.Symbols), &entry.Symbols); err != nil {
			return nil, fmt.Errorf("decode series timeline row %d: %w", row.ID, err)
		}
		// 空列表统一读成 nil，与写入前的值一致。
		if len(entry.Series) == 0 {
			entry.Series = nil
		}
		if len(entry.OutputFields) == 0 {
			entry.OutputFields = nil
		}
		if len(entry.Symbols) == 0 {
			entry.Symbols = nil
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
			EffectiveFrom: entry.EffectiveFrom, SeriesHash: entry.SeriesHash, LatenessS: entry.LatenessS, CreatedAt: nowUnixMilli(),
		}
		if row.SpaceID == "" || row.DatasetID == "" || row.Frequency == "" || row.EffectiveFrom <= 0 {
			return fmt.Errorf("series timeline entry needs space_id, dataset_id, frequency and a positive effective_from")
		}
		series, outputFields, symbols := entry.Series, entry.OutputFields, entry.Symbols
		if series == nil {
			series = []seriesset.Series{}
		}
		if outputFields == nil {
			outputFields = []string{}
		}
		if symbols == nil {
			symbols = map[string]string{}
		}
		rawSeries, err := json.Marshal(series)
		if err != nil {
			return err
		}
		rawFields, err := json.Marshal(outputFields)
		if err != nil {
			return err
		}
		rawSymbols, err := json.Marshal(symbols)
		if err != nil {
			return err
		}
		row.Series, row.OutputFields, row.Symbols = string(rawSeries), string(rawFields), string(rawSymbols)
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
