package domain

import "time"

// OutputManifest records the dynamic result RowKeys owned by one binding task.
type OutputManifest struct {
	BindingID   string    `gorm:"column:c_binding_id;primaryKey"`
	SubjectID   string    `gorm:"column:c_subject_id;primaryKey"`
	Frequency   string    `gorm:"column:c_frequency;primaryKey"`
	PeriodTime  int64     `gorm:"column:c_period_time;primaryKey"`
	RowKeysJSON string    `gorm:"column:c_row_keys_json"`
	UpdatedAt   time.Time `gorm:"column:c_updated_at"`
}

func (OutputManifest) TableName() string { return "t_factor_output_manifests" }
