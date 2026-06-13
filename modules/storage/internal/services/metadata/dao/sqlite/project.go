package sqlite

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Project 项目表定义
type Project model.Project

// TableName 指定表名
func (p Project) TableName() string {
	return model.ProjectTableName
}

// AddProject 添加项目
func (d *dataDBImpl) AddProject(projID int, projName string, remark string) error {
	project := &model.Project{
		ProjID:     projID,
		ProjName:   projName,
		Remark:     remark,
		Enabled:    constants.EnabledValue,
		CreateTime: time.Now(),
		ModifyTime: time.Now(),
	}
	result := d.db.Create(project)
	if result.Error != nil {
		log.Errorf("AddProject err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetProjectByID 根据ID获取项目
func (d *dataDBImpl) GetProjectByID(projID int) (*model.Project, error) {
	var project model.Project
	result := d.db.Where("c_proj_id = ? AND c_enabled = ?", projID, constants.EnabledValue).First(&project)
	if result.Error != nil {
		log.Errorf("GetProjectByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &project, nil
}

// GetProjectList 获取项目列表（外显）
func (d *dataDBImpl) GetProjectList() ([]model.Project, error) {
	var projects []model.Project
	result := d.db.Where("c_enabled = ? AND c_is_hide =0 ", constants.EnabledValue).Find(&projects) // c_is_hide==0只显示能外显的
	if result.Error != nil {
		log.Errorf("GetProjectList err[%v]", result.Error)
		return nil, result.Error
	}
	return projects, nil
}

// UpdateProject 更新项目
func (d *dataDBImpl) UpdateProject(projID int, projName string, remark string) error {
	result := d.db.Model(&model.Project{}).
		Where("c_proj_id = ? AND c_enabled = ?", projID, constants.EnabledValue).
		Updates(map[string]any{
			"c_proj_name": projName,
			"c_remark":    remark,
			"c_mtime":     time.Now(),
		})
	if result.Error != nil {
		log.Errorf("UpdateProject err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteProject 删除项目
func (d *dataDBImpl) DeleteProject(projID int) error {
	result := d.db.Model(&model.Project{}).
		Where("c_proj_id = ? AND c_enabled = ?", projID, constants.EnabledValue).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteProject err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetMaxProjectID 获取当前最大的项目ID
func (d *dataDBImpl) GetMaxProjectID() (int, error) {
	var maxID int
	err := d.db.Table("t_project").Select("COALESCE(MAX(c_proj_id), 0)").Scan(&maxID).Error
	if err != nil {
		return 0, fmt.Errorf("获取最大项目ID失败: %w", err)
	}
	return maxID, nil
}
