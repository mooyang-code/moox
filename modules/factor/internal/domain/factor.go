package domain

import "time"

const (
	FactorKindTimeseries     = "timeseries"
	FactorKindCrossSection   = "cross_section"
	FactorStatusEnabled      = "enabled"
	FactorStatusDisabled     = "disabled"
	DefaultFactorParamsJSON  = "[]"
	DefaultFactorDependsJSON = "[]"
)

// FactorDef is a locally managed factor definition.
type FactorDef struct {
	FactorID      string    `gorm:"column:c_factor_id;primaryKey"`
	Name          string    `gorm:"column:c_name"`
	Kind          string    `gorm:"column:c_kind"`
	SourceCode    string    `gorm:"column:c_source_code"`
	SourceHash    string    `gorm:"column:c_source_hash"`
	ParamsJSON    string    `gorm:"column:c_params_json"`
	LookbackBars  int       `gorm:"column:c_lookback_bars"`
	WritebackBars int       `gorm:"column:c_writeback_bars"`
	DependsJSON   string    `gorm:"column:c_depends_json"`
	Status        string    `gorm:"column:c_status"`
	CreateTime    time.Time `gorm:"column:c_ctime"`
	ModifyTime    time.Time `gorm:"column:c_mtime"`
}

// TableName returns the factor definition table.
func (FactorDef) TableName() string {
	return "t_factor_defs"
}
