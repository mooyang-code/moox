package domain

import "time"

const (
	FactorStatusEnabled  = "enabled"
	FactorStatusDisabled = "disabled"
)

// FactorDef is a locally managed factor definition.
type FactorDef struct {
	FactorID     string    `gorm:"column:c_factor_id;primaryKey"`
	Name         string    `gorm:"column:c_name"`
	SourceCode   string    `gorm:"column:c_source_code"`
	SourceHash   string    `gorm:"column:c_source_hash"`
	SourcePath   string    `gorm:"column:c_source_path"`
	Periods      []int     `gorm:"column:c_periods_json;serializer:json"`
	LookbackBars int       `gorm:"column:c_lookback_bars"`
	Depends      []string  `gorm:"column:c_depends_json;serializer:json"`
	Status       string    `gorm:"column:c_status"`
	CreateTime   time.Time `gorm:"column:c_ctime"`
	ModifyTime   time.Time `gorm:"column:c_mtime"`
}

// TableName returns the factor definition table.
func (FactorDef) TableName() string {
	return "t_factor_defs"
}
