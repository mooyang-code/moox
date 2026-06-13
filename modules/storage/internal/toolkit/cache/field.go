package cache

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"trpc.group/trpc-go/trpc-go/log"
)

const TBField = "t_field"

// Field 字段表结构
type Field struct {
	// ID 自增主键
	ID uint `json:"id"`
	// FieldID 字段ID
	FieldID int `json:"field_id"`
	// ProjID 项目ID
	ProjID int `json:"proj_id"`
	// DatasetIDs 数据集ID列表
	DatasetIDs string `json:"dataset_ids"`
	// FieldName 字段名称(中文名)
	FieldName string `json:"field_name"`
	// InterfaceName 接口名称
	InterfaceName string `json:"interface_name"`

	// Desc 字段描述
	Desc string `json:"desc"`
	// TableType 字段所属表类型（1=数据对象表，2=数据表）
	TableType int `json:"table_type"`
	// Required 是否必填字段（"true"=是，"false"=否）
	Required string `json:"required"`
	// Unique 是否唯一字段（"true"=是，"false"=否）
	Unique string `json:"unique"`
	// ParentFieldID 父字段ID
	ParentFieldID int `json:"parent_field_id"`
	// LevelInfo 层级信息
	LevelInfo string `json:"level_info"`
	// FieldPrimaryFormat 字段主要格式
	FieldPrimaryFormat int `json:"field_primary_format"`
	// FieldSecondaryFormat 字段次要格式
	FieldSecondaryFormat int `json:"field_secondary_format"`
	// ValueLibID 值库ID
	ValueLibID int `json:"value_lib_id"`
	// ValidationRule 验证规则(正则表达式)
	ValidationRule string `json:"validation_rule"`
	// WriteExample 写入示例
	WriteExample string `json:"write_example"`
	// Remark 备注信息
	Remark string `json:"remark"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
}

// SchemaID 实现接口TableCacher
func (Field) SchemaID() string {
	return TBField
}

// URL 实现接口TableCacher
func (f Field) URL() string {
	return f.AccessUrl
}

// SearchFields 实现接口TableCacher
func (Field) SearchFields() map[string]string {
	return map[string]string{TBField: "field_id"}
}

// FilterKey 实现接口TableCacher
func (Field) FilterKey() string {
	return "enabled=true"
}

// GetFieldInfoByID 获取字段配置缓存（通过字段ID）
func GetFieldInfoByID(fieldID int) *Field {
	field, ok := QueryDataItem(TBField, "field_id="+strconv.Itoa(fieldID)).(*Field)
	if !ok {
		return nil
	}
	return field
}

// GetAllFieldInfo 获取所有字段配置缓存
func GetAllFieldInfo() []*Field {
	fields, ok := GetAll(TBField).([]*Field)
	if !ok {
		return nil
	}
	return fields
}

// GetMetaFieldList 获取数据对象字段列表（table_type=1）
func GetMetaFieldList(ctx context.Context, projID, datasetID int32) ([]*Field, error) {
	// 获取所有字段信息
	allFields := GetAllFieldInfo()
	if len(allFields) == 0 {
		return nil, fmt.Errorf("no field info found")
	}

	// 在内存中筛选满足条件的字段
	var metaFields []*Field
	for _, field := range allFields {
		// 过滤条件：projID匹配、datasetID匹配且table_type=1（数据对象表）
		if field.ProjID == int(projID) && isFieldInDataset(field, int(datasetID)) && field.TableType == 1 {
			metaFields = append(metaFields, field)
		}
	}

	if len(metaFields) == 0 {
		log.InfoContextf(ctx, "No meta fields found for proj_id=%d, dataset_id=%d", projID, datasetID)
	}
	return metaFields, nil
}

// GetDetailFieldList 获取数据详情字段列表（table_type!=1）
func GetDetailFieldList(ctx context.Context, projID, datasetID int32) ([]*Field, error) {
	// 获取所有字段信息
	allFields := GetAllFieldInfo()
	if len(allFields) == 0 {
		return nil, fmt.Errorf("no field info found")
	}

	// 在内存中筛选满足条件的字段
	var detailFields []*Field
	for _, field := range allFields {
		// 过滤条件：projID匹配、datasetID匹配且table_type!=1（不是数据对象表）
		if field.ProjID == int(projID) && isFieldInDataset(field, int(datasetID)) && field.TableType != 1 {
			detailFields = append(detailFields, field)
		}
	}
	if len(detailFields) == 0 {
		log.InfoContextf(ctx, "No detail fields found for proj_id=%d, dataset_id=%d", projID, datasetID)
	}
	return detailFields, nil
}

// BuildFieldName2IDMapping 构建项目下字段接口名到字段ID的映射（包含系统字段）
func BuildFieldName2IDMapping(projID int32) map[string]uint32 {
	fieldNameToID := make(map[string]uint32)

	// 首先添加系统字段映射
	for fieldName, fieldID := range constants.SystemFieldName2ID {
		fieldNameToID[fieldName] = fieldID
	}

	// 获取所有字段信息
	allFields := GetAllFieldInfo()
	if len(allFields) == 0 {
		return fieldNameToID
	}

	// 创建用户定义字段名到字段ID的映射
	for _, field := range allFields {
		if field.ProjID == int(projID) {
			fieldNameToID[field.InterfaceName] = uint32(field.FieldID)
		}
	}
	return fieldNameToID
}

// BuildFieldID2NameMapping 构建项目下字段ID到字段接口名的映射（包含系统字段）
func BuildFieldID2NameMapping(projID int32) map[uint32]string {
	fieldIDToName := make(map[uint32]string)

	// 首先添加系统字段映射
	for fieldID, fieldName := range constants.SystemFieldID2Name {
		fieldIDToName[fieldID] = fieldName
	}

	// 获取所有字段信息
	allFields := GetAllFieldInfo()
	if len(allFields) == 0 {
		return fieldIDToName
	}

	// 创建用户定义字段ID到字段名的映射
	for _, field := range allFields {
		if field.ProjID == int(projID) {
			fieldIDToName[uint32(field.FieldID)] = field.InterfaceName
		}
	}
	return fieldIDToName
}

// 检查字段是否属于指定数据集
func isFieldInDataset(field *Field, datasetID int) bool {
	// 字段可能属于多个数据集，DatasetIDs字段可能是用+分隔的数据集ID列表
	if field.DatasetIDs == "" {
		return false
	}

	// 检查是否包含特定的数据集ID
	for _, id := range strings.SplitN(field.DatasetIDs, "+", -1) {
		// 去除可能的空格
		id = strings.TrimSpace(id)
		if id == "*" || id == fmt.Sprintf("%d", datasetID) {
			return true
		}
	}
	return false
}
