package cache

const TBFieldColumnMap = "t_field_column_map"

// FieldColumnMap 字段-DB列名映射表结构
type FieldColumnMap struct {
	// ID 自增ID
	ID int `json:"_id"`
	// ProjectID 项目ID
	ProjectID int `json:"project_id"`
	// TableType 表类型（1数据对象表；2数据表）
	TableType int `json:"table_type"`
	// FieldID 字段ID
	FieldID int `json:"field_id"`
	// ColumnName 底层列名
	ColumnName string `json:"column_name"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
}

// SchemaID 实现接口TableCacher
func (FieldColumnMap) SchemaID() string {
	return TBFieldColumnMap
}

// URL 实现接口TableCacher
func (f FieldColumnMap) URL() string {
	return f.AccessUrl
}

// SearchFields 实现接口TableCacher
func (FieldColumnMap) SearchFields() map[string]string {
	return map[string]string{TBFieldColumnMap: "_id"}
}

// FilterKey 实现接口TableCacher
func (FieldColumnMap) FilterKey() string {
	return "field_id>0" // 字段列名映射表没有删除标记，不需要过滤
}

// GetAllFieldColumnMaps 获取所有字段列名映射缓存
func GetAllFieldColumnMaps() []*FieldColumnMap {
	appFields, ok := GetAll(TBFieldColumnMap).([]*FieldColumnMap)
	if !ok {
		return nil
	}
	return appFields
}

// GetColumnNameByFieldID 根据字段ID、项目ID和表类型获取列名
func GetColumnNameByFieldID(projectID, tableType, fieldID int) string {
	allMaps := GetAllFieldColumnMaps()
	if allMaps == nil {
		return ""
	}

	for _, m := range allMaps {
		if m.FieldID == fieldID && m.ProjectID == projectID && m.TableType == tableType {
			return m.ColumnName
		}
	}
	return ""
}

// GetFieldIDByColumnName 根据项目ID、表类型和列名获取字段ID列表
func GetFieldIDByColumnName(projectID, tableType int, columnName string) int {
	allMaps := GetAllFieldColumnMaps()
	if allMaps == nil {
		return 0
	}

	for _, m := range allMaps {
		if m.ProjectID == projectID && m.TableType == tableType && m.ColumnName == columnName {
			return m.FieldID
		}
	}
	return 0
}
