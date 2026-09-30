package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

var ErrPeriodStorageStateConflict = errors.New("collector period Storage state conflict")

type periodStorageStateRow struct {
	SpaceID       string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID     string    `gorm:"column:c_dataset_id;primaryKey"`
	Frequency     string    `gorm:"column:c_frequency;primaryKey"`
	PeriodTime    time.Time `gorm:"column:c_period_time;primaryKey"`
	SeriesHash    string    `gorm:"column:c_series_hash"`
	ExpectedCount uint32    `gorm:"column:c_expected_count"`
	DeadlineAt    time.Time `gorm:"column:c_deadline_at"`
	Status        string    `gorm:"column:c_status"`
	ConfirmedAt   time.Time `gorm:"column:c_confirmed_at"`
}

func (periodStorageStateRow) TableName() string { return "t_collector_period_storage_states" }

// PeriodStorageStateRepository stores successful authoritative Storage
// observations. Terminal states are monotonic and the first Storage deadline
// is immutable.
type PeriodStorageStateRepository struct{ db *gorm.DB }

func NewPeriodStorageStateRepository(db *gorm.DB) *PeriodStorageStateRepository {
	return &PeriodStorageStateRepository{db: db}
}

func (r *PeriodStorageStateRepository) ObservePeriodStorageState(ctx context.Context, state domain.PeriodStorageState) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("period Storage state repository is not initialized")
	}
	state.Normalize()
	key, err := normalizePeriodSeriesSnapshotKey(state.Key)
	if err != nil {
		return err
	}
	state.Key = key
	if state.SeriesHash == "" || state.ExpectedCount == 0 || state.DeadlineAt.IsZero() || state.ConfirmedAt.IsZero() {
		return fmt.Errorf("period Storage state hash, expected_count, deadline_at and confirmed_at are required")
	}
	if !validPeriodStorageStatus(state.Status) {
		return fmt.Errorf("unsupported period Storage status %q", state.Status)
	}
	row := periodStorageStateRow{
		SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime,
		SeriesHash: state.SeriesHash, ExpectedCount: state.ExpectedCount, DeadlineAt: state.DeadlineAt,
		Status: state.Status, ConfirmedAt: state.ConfirmedAt,
	}
	result := r.db.WithContext(ctx).Exec(`
INSERT INTO t_collector_period_storage_states (
    c_space_id, c_dataset_id, c_frequency, c_period_time, c_series_hash,
    c_expected_count, c_deadline_at, c_status, c_confirmed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (c_space_id, c_dataset_id, c_frequency, c_period_time) DO UPDATE SET
    c_status = CASE
        WHEN t_collector_period_storage_states.c_status IN (?, ?) THEN t_collector_period_storage_states.c_status
        ELSE excluded.c_status
    END,
    c_confirmed_at = CASE
        WHEN t_collector_period_storage_states.c_status IN (?, ?) THEN t_collector_period_storage_states.c_confirmed_at
        ELSE excluded.c_confirmed_at
    END
WHERE t_collector_period_storage_states.c_series_hash = excluded.c_series_hash
  AND t_collector_period_storage_states.c_expected_count = excluded.c_expected_count
  AND t_collector_period_storage_states.c_deadline_at = excluded.c_deadline_at
  AND (
      t_collector_period_storage_states.c_status = ?
      OR excluded.c_status = ?
      OR excluded.c_status = t_collector_period_storage_states.c_status
  )`,
		row.SpaceID, row.DatasetID, row.Frequency, row.PeriodTime, row.SeriesHash,
		row.ExpectedCount, row.DeadlineAt, row.Status, row.ConfirmedAt,
		domain.PeriodStatusComplete, domain.PeriodStatusDegraded,
		domain.PeriodStatusComplete, domain.PeriodStatusDegraded,
		domain.PeriodStatusWaiting, domain.PeriodStatusWaiting,
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPeriodStorageStateConflict
	}
	return nil
}

func (r *PeriodStorageStateRepository) GetPeriodStorageState(ctx context.Context, input domain.PeriodKey) (domain.PeriodStorageState, bool, error) {
	if r == nil || r.db == nil {
		return domain.PeriodStorageState{}, false, fmt.Errorf("period Storage state repository is not initialized")
	}
	key, err := normalizePeriodSeriesSnapshotKey(input)
	if err != nil {
		return domain.PeriodStorageState{}, false, err
	}
	var row periodStorageStateRow
	err = r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.PeriodStorageState{}, false, nil
	}
	if err != nil {
		return domain.PeriodStorageState{}, false, err
	}
	return domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, PeriodTime: row.PeriodTime.UTC()},
		SeriesHash: row.SeriesHash, ExpectedCount: row.ExpectedCount, DeadlineAt: row.DeadlineAt.UTC(),
		Status: row.Status, ConfirmedAt: row.ConfirmedAt.UTC(),
	}, true, nil
}

func validPeriodStorageStatus(status string) bool {
	return status == domain.PeriodStatusWaiting || status == domain.PeriodStatusComplete || status == domain.PeriodStatusDegraded
}
