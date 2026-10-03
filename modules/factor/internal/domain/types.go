package domain

import "time"

const (
	SubjectModeAll     = "all"
	SubjectModeInclude = "include"

	SetStatusPending  = "pending"
	SetStatusEnabled  = "enabled"
	SetStatusDisabled = "disabled"
)

// FactorSet groups definitions over one source dataset and frequency.
type FactorSet struct {
	SetID           string    `json:"set_id" gorm:"column:c_set_id;primaryKey"`
	SpaceID         string    `json:"space_id" gorm:"column:c_space_id"`
	SourceDatasetID string    `json:"source_dataset_id" gorm:"column:c_source_dataset_id"`
	Freq            string    `json:"freq" gorm:"column:c_freq"`
	SubjectMode     string    `json:"subject_mode" gorm:"column:c_subject_mode"`
	Subjects        []string  `json:"subjects" gorm:"column:c_subjects_json;serializer:json"`
	ResultDatasetID string    `json:"result_dataset_id" gorm:"column:c_result_dataset_id"`
	Status          string    `json:"status" gorm:"column:c_status"`
	CreatedAt       time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorSet) TableName() string { return "t_factor_sets" }
