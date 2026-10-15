package store

import (
	"context"
	"time"
)

// HistoryScan 是一个数据集（某个频率）最近一次历史缺口扫描的结果。
type HistoryScan struct {
	SpaceID   string
	DatasetID string
	Frequency string
	// ScannedAt 是最近一次扫描完成或失败的时间，决定下一次扫描。
	ScannedAt time.Time
	// From、To 是读回的范围；扫描失败时为零值。
	From time.Time
	To   time.Time
	// GapBars、GapSubjects 是扣除已确认缺失后的缺口根数与受影响标的数。
	GapBars     int
	GapSubjects int
	// Gaps 是缺口分组 JSON，供确认补采精确提交。
	Gaps      string
	LastError string
}

type historyScanRow struct {
	SpaceID     string     `gorm:"column:c_space_id;primaryKey"`
	DatasetID   string     `gorm:"column:c_dataset_id;primaryKey"`
	Frequency   string     `gorm:"column:c_frequency;primaryKey"`
	ScannedAt   time.Time  `gorm:"column:c_scanned_at"`
	From        *time.Time `gorm:"column:c_from"`
	To          *time.Time `gorm:"column:c_to"`
	GapBars     int        `gorm:"column:c_gap_bars"`
	GapSubjects int        `gorm:"column:c_gap_subjects"`
	Gaps        string     `gorm:"column:c_gaps"`
	LastError   string     `gorm:"column:c_last_error"`
}

func (historyScanRow) TableName() string { return "t_collector_history_scans" }

func (row historyScanRow) scan() HistoryScan {
	scan := HistoryScan{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, ScannedAt: row.ScannedAt, GapBars: row.GapBars, GapSubjects: row.GapSubjects, Gaps: row.Gaps, LastError: row.LastError}
	if row.From != nil {
		scan.From = *row.From
	}
	if row.To != nil {
		scan.To = *row.To
	}
	return scan
}

// HistoryScans 返回空间全部数据集的历史扫描记录，键为“数据集|频率”。
func (r *BackfillRepository) HistoryScans(ctx context.Context, spaceID string) (map[string]HistoryScan, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var rows []historyScanRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", spaceID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]HistoryScan, len(rows))
	for _, row := range rows {
		out[row.DatasetID+"|"+row.Frequency] = row.scan()
	}
	return out, nil
}

// SaveHistoryScan 写入（覆盖）一个数据集的历史扫描记录。
func (r *BackfillRepository) SaveHistoryScan(ctx context.Context, scan HistoryScan) error {
	if err := r.ready(); err != nil {
		return err
	}
	row := historyScanRow{SpaceID: scan.SpaceID, DatasetID: scan.DatasetID, Frequency: scan.Frequency, ScannedAt: scan.ScannedAt.UTC(), GapBars: scan.GapBars, GapSubjects: scan.GapSubjects, Gaps: scan.Gaps, LastError: scan.LastError}
	if row.Gaps == "" {
		row.Gaps = "[]"
	}
	if !scan.From.IsZero() {
		from := scan.From.UTC()
		row.From = &from
	}
	if !scan.To.IsZero() {
		to := scan.To.UTC()
		row.To = &to
	}
	return r.db.WithContext(ctx).Save(&row).Error
}
