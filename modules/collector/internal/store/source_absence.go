package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SourceAbsence 是已确认“行情源没有”的一段连续 K 线：[From, To) 共 Bars 根。
type SourceAbsence struct {
	SpaceID   string
	DatasetID string
	Frequency string
	SubjectID string
	From      time.Time
	To        time.Time
	Bars      int
	CreatedAt time.Time
}

type sourceAbsenceRow struct {
	SpaceID   string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID string    `gorm:"column:c_dataset_id;primaryKey"`
	Frequency string    `gorm:"column:c_frequency;primaryKey"`
	SubjectID string    `gorm:"column:c_subject_id;primaryKey"`
	From      time.Time `gorm:"column:c_from;primaryKey"`
	To        time.Time `gorm:"column:c_to"`
	Bars      int       `gorm:"column:c_bars"`
	CreatedAt time.Time `gorm:"column:c_ctime"`
}

func (sourceAbsenceRow) TableName() string { return "t_collector_source_absences" }

// AddAbsences 记录已确认的行情源缺失；同一起点重复确认时取更长的区间。
func (r *BackfillRepository) AddAbsences(ctx context.Context, absences []SourceAbsence) error {
	if err := r.ready(); err != nil {
		return err
	}
	if len(absences) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, absence := range absences {
			if !absence.From.Before(absence.To) {
				return fmt.Errorf("absence range of %s is empty", absence.SubjectID)
			}
			if err := tx.Exec(`INSERT INTO t_collector_source_absences (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_bars, c_ctime) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from) DO UPDATE SET c_to = MAX(c_to, excluded.c_to), c_bars = MAX(c_bars, excluded.c_bars)`,
				absence.SpaceID, absence.DatasetID, absence.Frequency, absence.SubjectID, absence.From.UTC(), absence.To.UTC(), absence.Bars, now).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// AbsencesIn 返回与 [from, to) 相交的已确认缺失；subjects 为空表示全部标的。
func (r *BackfillRepository) AbsencesIn(ctx context.Context, spaceID, datasetID, frequency string, subjects []string, from, to time.Time) ([]SourceAbsence, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_from < ? AND c_to > ?", spaceID, datasetID, frequency, to.UTC(), from.UTC())
	if len(subjects) > 0 {
		query = query.Where("c_subject_id IN ?", subjects)
	}
	var rows []sourceAbsenceRow
	if err := query.Order("c_subject_id, c_from").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]SourceAbsence, 0, len(rows))
	for _, row := range rows {
		out = append(out, SourceAbsence{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, SubjectID: row.SubjectID, From: row.From, To: row.To, Bars: row.Bars, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

// AbsentBars 返回空间每个“数据集|频率”已确认缺失的总根数。
func (r *BackfillRepository) AbsentBars(ctx context.Context, spaceID string) (map[string]int, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	type sumRow struct {
		DatasetID string `gorm:"column:c_dataset_id"`
		Frequency string `gorm:"column:c_frequency"`
		Bars      int    `gorm:"column:n"`
	}
	var rows []sumRow
	if err := r.db.WithContext(ctx).Model(&sourceAbsenceRow{}).Select("c_dataset_id, c_frequency, SUM(c_bars) AS n").Where("c_space_id = ?", spaceID).Group("c_dataset_id, c_frequency").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.DatasetID+"|"+row.Frequency] = row.Bars
	}
	return out, nil
}

// ClearAbsences 清掉一个数据集（某个频率）全部已确认缺失，并删除其历史扫描记录让下一轮重新扫描；返回清掉的区间数。
func (r *BackfillRepository) ClearAbsences(ctx context.Context, spaceID, datasetID, frequency string) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	spaceID, datasetID, frequency = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.TrimSpace(frequency)
	if spaceID == "" || datasetID == "" || frequency == "" {
		return 0, fmt.Errorf("space_id, dataset_id and frequency are required")
	}
	var cleared int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", spaceID, datasetID, frequency).Delete(&sourceAbsenceRow{})
		if result.Error != nil {
			return result.Error
		}
		cleared = result.RowsAffected
		return tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", spaceID, datasetID, frequency).Delete(&historyScanRow{}).Error
	})
	return cleared, err
}
