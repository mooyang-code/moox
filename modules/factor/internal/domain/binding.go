package domain

import "time"

const (
	SubjectModeAll         = "all"
	SubjectModeInclude     = "include"
	BindingStatusEnabled   = "enabled"
	BindingStatusDisabled  = "disabled"
	DefaultSubjectsJSON    = "[]"
	DefaultBindingTargetID = ""
)

// FactorBinding binds a factor to a source dataset/frequency scope.
type FactorBinding struct {
	BindingID     string    `gorm:"column:c_binding_id;primaryKey"`
	FactorID      string    `gorm:"column:c_factor_id"`
	SpaceID       string    `gorm:"column:c_space_id"`
	SourceDataset string    `gorm:"column:c_source_dataset"`
	Freq          string    `gorm:"column:c_freq"`
	SubjectMode   string    `gorm:"column:c_subject_mode"`
	SubjectsJSON  string    `gorm:"column:c_subjects_json"`
	TargetDataset string    `gorm:"column:c_target_dataset"`
	Status        string    `gorm:"column:c_status"`
	CreateTime    time.Time `gorm:"column:c_ctime"`
	ModifyTime    time.Time `gorm:"column:c_mtime"`
}

// TableName returns the factor binding table.
func (FactorBinding) TableName() string {
	return "t_factor_bindings"
}
