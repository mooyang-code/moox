package cache

import (
	"fmt"
	"strconv"
	"strings"
)

const TBDataset = "t_dataset"

// Dataset 数据集表结构
type Dataset struct {
	// DatasetID 数据集ID
	DatasetID int `json:"dataset_id"`
	// DatasetName 数据集名
	DatasetName string `json:"dataset_name"`
	// ObjectTableID 数据集对应的对象表ID
	ObjectTableID string `json:"object_table_id"`
	// DataTableID 数据集对应的数据表ID
	DataTableID string `json:"data_table_id"`
	// ProjID 所属项目ID
	ProjID int `json:"proj_id"`
	// DataType 数据类型（取值见:common.proto-EnumDataType）
	DataType int `json:"data_type"`
	// Freqs 时序周期（多值用+分割）
	Freqs string `json:"freqs"`
	// CheckRules 数据完整性校验规则（内置的校验规则名）
	CheckRules string `json:"check_rules"`
	// Comment 备注
	Comment string `json:"comment"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
}

// SchemaID 实现接口TableCacher
func (Dataset) SchemaID() string {
	return TBDataset
}

// URL 实现接口TableCacher
func (d Dataset) URL() string {
	return d.AccessUrl
}

// SearchFields 实现接口TableCacher
func (Dataset) SearchFields() map[string]string {
	return map[string]string{TBDataset: "dataset_id"}
}

// FilterKey 实现接口TableCacher
func (Dataset) FilterKey() string {
	return "enabled=true"
}

// GetDatasetInfo 获取数据集配置缓存
func GetDatasetInfo(datasetID int) *Dataset {
	datasetInfo, ok := QueryDataItem(TBDataset, "dataset_id="+strconv.Itoa(datasetID)).(*Dataset)
	if !ok {
		return nil
	}
	return datasetInfo
}

// GetAllDatasetInfo 获取所有数据集配置缓存
func GetAllDatasetInfo() []*Dataset {
	datasets, ok := GetAll(TBDataset).([]*Dataset)
	if !ok {
		return nil
	}
	return datasets
}

// GetDatasetByProjID 根据项目ID获取数据集列表
func GetDatasetByProjID(projID int) []*Dataset {
	allDatasets := GetAllDatasetInfo()
	if allDatasets == nil {
		return nil
	}

	var result []*Dataset
	for _, dataset := range allDatasets {
		if dataset.ProjID == projID {
			result = append(result, dataset)
		}
	}
	return result
}

// GetDatasetByDataType 根据数据类型获取数据集列表
func GetDatasetByDataType(dataType int) []*Dataset {
	allDatasets := GetAllDatasetInfo()
	if allDatasets == nil {
		return nil
	}

	var result []*Dataset
	for _, dataset := range allDatasets {
		if dataset.DataType == dataType {
			result = append(result, dataset)
		}
	}
	return result
}

// GetDatasetByID 根据数据集ID获取数据集信息
func GetDatasetByID(datasetID int) (*Dataset, error) {
	dataset := GetDatasetInfo(datasetID)
	if dataset == nil {
		return nil, fmt.Errorf("数据集[%d]不存在", datasetID)
	}
	return dataset, nil
}

// GetFieldsByProject 获取指定项目下的所有字段
func GetFieldsByProject(projID int) []*Field {
	allFields := GetAllFieldInfo()
	if allFields == nil {
		return nil
	}

	var result []*Field
	for _, field := range allFields {
		if field.ProjID == projID {
			result = append(result, field)
		}
	}
	return result
}

// GetProjectIDByTableID 根据tableID查询项目ID
// tableID格式：t_object_datasetID 或 t_data_datasetID_objectID_freq
// 通过匹配t_dataset中的c_object_table_id、c_data_table_id的值与tableID前缀来获取项目ID
func GetProjectIDByTableID(tableID string) (int, error) {
	if tableID == "" {
		return 0, fmt.Errorf("tableID不能为空")
	}

	// 获取所有数据集信息
	allDatasets := GetAllDatasetInfo()
	if allDatasets == nil {
		return 0, fmt.Errorf("无法获取数据集信息")
	}

	// 遍历所有数据集，匹配tableID前缀
	for _, dataset := range allDatasets {
		// 检查对象表ID匹配（前缀匹配）
		if dataset.ObjectTableID != "" && strings.HasPrefix(tableID, dataset.ObjectTableID) {
			return dataset.ProjID, nil
		}

		// 检查数据表ID匹配（前缀匹配）
		if dataset.DataTableID != "" && strings.HasPrefix(tableID, dataset.DataTableID) {
			return dataset.ProjID, nil
		}
	}
	return 0, fmt.Errorf("未找到tableID[%s]对应的项目ID", tableID)
}
