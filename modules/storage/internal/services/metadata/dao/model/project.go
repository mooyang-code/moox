package model

import "time"

// Project 项目表定义
type Project struct {
	// ID 自增主键
	ID uint `gorm:"primaryKey;column:c_id;autoIncrement" json:"id" yaml:"id"`
	// ProjID 项目ID
	ProjID int `gorm:"column:c_proj_id;uniqueIndex:idx_proj_id;not null;default:0" json:"proj_id" yaml:"proj_id"`
	// ProjName 项目名称
	ProjName string `gorm:"column:c_proj_name;index:idx_proj_name;size:100;not null;default:''" json:"proj_name" yaml:"proj_name"`
	// Remark 备注
	Remark string `gorm:"column:c_remark;type:text" json:"remark" yaml:"remark"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `gorm:"column:c_enabled;type:text;not null;default:'true'" json:"enabled" yaml:"enabled"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time" yaml:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time" yaml:"modify_time"`
}

const ProjectTableName = "t_project"

// TableName 指定表名
func (p *Project) TableName() string {
	return ProjectTableName
}
