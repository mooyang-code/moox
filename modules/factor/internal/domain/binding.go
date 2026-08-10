package domain

import "time"

const (
	SubjectModeAll              = "all"
	SubjectModeInclude          = "include"
	BindingStatusPendingView    = "pending_view"
	BindingStatusEnabled        = "enabled"
	BindingStatusDisabled       = "disabled"
	BindingStatusCleanupPending = "cleanup_pending"
	DefaultSubjectsJSON         = "[]"
	// Deprecated: retained for in-process callers during the View migration.
	DefaultBindingTargetID = "__default__"
)

// FactorBinding binds a factor to a source View/frequency scope.
type FactorBinding struct {
	BindingID       string    `gorm:"column:c_binding_id;primaryKey"`
	FactorID        string    `gorm:"column:c_factor_id"`
	SpaceID         string    `gorm:"column:c_space_id"`
	SourceViewID    string    `gorm:"column:c_source_view_id"`
	Freq            string    `gorm:"column:c_freq"`
	SubjectMode     string    `gorm:"column:c_subject_mode"`
	SubjectsJSON    string    `gorm:"column:c_subjects_json"`
	ResultDatasetID string    `gorm:"column:c_result_dataset_id"`
	ResultViewID    string    `gorm:"column:c_result_view_id"`
	SourceDataset   string    `gorm:"-"`
	TargetDataset   string    `gorm:"-"`
	Status          string    `gorm:"column:c_status"`
	CreateTime      time.Time `gorm:"column:c_ctime"`
	ModifyTime      time.Time `gorm:"column:c_mtime"`
}

// TableName returns the factor binding table.
func (FactorBinding) TableName() string {
	return "t_factor_bindings"
}
