package sqlite

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dataset 数据集定义表
type Dataset model.Dataset

// TableName 指定表名
func (d Dataset) TableName() string {
	return model.DatasetTableName
}

// GetDatasetList 获取所有数据集列表
func (d *dataDBImpl) GetDatasetList() ([]model.Dataset, error) {
	var datasets []model.Dataset
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&datasets)
	if result.Error != nil {
		log.Errorf("GetDatasetList err[%v]", result.Error)
		return nil, result.Error
	}
	return datasets, nil
}

// GetDatasetByID 根据ID获取数据集
func (d *dataDBImpl) GetDatasetByID(datasetID int) (*model.Dataset, error) {
	var dataset model.Dataset
	result := d.db.Where("c_dataset_id = ? AND c_enabled = ?", datasetID, constants.EnabledValue).First(&dataset)
	if result.Error != nil {
		log.Errorf("GetDatasetByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &dataset, nil
}

// GetDatasetByProjID 根据项目ID获取数据集列表
func (d *dataDBImpl) GetDatasetByProjID(projID int) ([]model.Dataset, error) {
	var datasets []model.Dataset
	result := d.db.Where("c_proj_id = ? AND c_enabled = ?", projID, constants.EnabledValue).Find(&datasets)
	if result.Error != nil {
		log.Errorf("GetDatasetByProjID err[%v]", result.Error)
		return nil, result.Error
	}
	return datasets, nil
}

// GetDatasetByDataType 根据数据类型获取数据集列表
func (d *dataDBImpl) GetDatasetByDataType(dataType int) ([]model.Dataset, error) {
	var datasets []model.Dataset
	result := d.db.Where("c_data_type = ? AND c_enabled = ?", dataType, constants.EnabledValue).Find(&datasets)
	if result.Error != nil {
		log.Errorf("GetDatasetByDataType err[%v]", result.Error)
		return nil, result.Error
	}
	return datasets, nil
}

// AddDataset 添加新的数据集
func (d *dataDBImpl) AddDataset(dataset *model.Dataset) error {
	if dataset.Enabled == "" {
		dataset.Enabled = constants.EnabledValue
	}
	result := d.db.Create(dataset)
	if result.Error != nil {
		log.Errorf("AddDataset err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateDataset 更新数据集信息
func (d *dataDBImpl) UpdateDataset(dataset *model.Dataset) error {
	result := d.db.Model(&model.Dataset{}).Where("c_dataset_id = ?", dataset.DatasetID).
		Updates(map[string]any{
			"c_dataset_name": dataset.DatasetName,
			"c_proj_id":      dataset.ProjID,
			"c_data_type":    dataset.DataType,
			"c_freqs":        dataset.Freqs,
			"c_check_rules":  dataset.CheckRules,
			"c_comment":      dataset.Comment,
		})
	if result.Error != nil {
		log.Errorf("UpdateDataset err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteDataset 逻辑删除数据集
func (d *dataDBImpl) DeleteDataset(datasetID int) error {
	result := d.db.Model(&model.Dataset{}).Where("c_dataset_id = ?", datasetID).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteDataset err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetMaxDatasetIDInRange 获取指定范围内的最大数据集ID
func (d *dataDBImpl) GetMaxDatasetIDInRange(minID, maxID int) (int, error) {
	var result int
	err := d.db.Table("t_dataset").
		Where("c_dataset_id >= ? AND c_dataset_id <= ?", minID, maxID).
		Select("COALESCE(MAX(c_dataset_id), ?)", minID-1).
		Scan(&result).Error
	if err != nil {
		return 0, fmt.Errorf("获取最大数据集ID失败: %w", err)
	}
	return result, nil
}
