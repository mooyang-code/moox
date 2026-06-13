// Package dao 列名映射更新逻辑
package dao

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	metadao "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	metamodel "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ProjectFieldGroups 项目字段分组
type ProjectFieldGroups struct {
	ProjectID    int
	ObjectFields []*cache.Field // 对象表字段 (TableType == 1)
	DataFields   []*cache.Field // 数据表字段 (TableType != 1)
}

// APIResponse 元数据API响应结构
type APIResponse struct {
	Code int           `json:"code"`
	Data []interface{} `json:"data"`
}

// FieldColumnMapResponse 字段列名映射响应结构
type FieldColumnMapResponse struct {
	ID         int    `json:"_id"`
	FieldID    int    `json:"field_id"`
	ProjectID  int    `json:"project_id"`
	TableType  int    `json:"table_type"`
	ColumnName string `json:"column_name"`
}

// BatchMappingItem 批量映射项结构
type BatchMappingItem struct {
	FieldID    int    `json:"field_id"`
	ProjectID  int    `json:"project_id"`
	TableType  int    `json:"table_type"`
	ColumnName string `json:"column_name"`
}

// FieldTypeGroup 按字段类型分组的字段
type FieldTypeGroup struct {
	FieldType pb.EnumFieldPrimaryFormat
	Fields    []*cache.Field
}

// ColumnStatus 列名状态
type ColumnStatus struct {
	ColumnName string
	IsUsed     bool
}

// ProjectTableColumns 项目表的所有列名状态
type ProjectTableColumns struct {
	ProjectID int
	TableType int
	Columns   map[string]*ColumnStatus // key: 列名, value: 状态
}

// ProjectTableKey 项目表类型组合键
type ProjectTableKey struct {
	ProjectID int
	TableType int
}

// ColumnMappingClient 列名映射客户端
type ColumnMappingClient struct {
	dbDAO metadao.DataInterfacer
}

// NewColumnMappingClient 创建列名映射客户端
func NewColumnMappingClient() (*ColumnMappingClient, error) {
	dbDAO, err := metadao.NewDataInterfacer()
	if err != nil {
		return nil, err
	}
	return &ColumnMappingClient{
		dbDAO: dbDAO,
	}, nil
}

// UpdateColumnMappings 执行一次完整的列名映射更新【定时任务调用，入口函数】
func UpdateColumnMappings(ctx context.Context, _ string) error {
	log.DebugContextf(ctx, "开始执行列名映射更新")

	// 1. 获取系统中所有字段配置
	allFields := cache.GetAllFieldInfo()
	if len(allFields) == 0 {
		log.InfoContextf(ctx, "系统中没有字段配置，跳过列名映射更新")
		return nil
	}
	log.DebugContextf(ctx, "获取到 %d 个字段配置", len(allFields))

	// 2. 通过本地DAO获取现有列名映射
	cli, err := NewColumnMappingClient()
	if err != nil {
		log.ErrorContextf(ctx, "初始化列名映射客户端失败(将等待下次定时任务重试): %v", err)
		return nil
	}
	existingMappings, err := cli.getAllColumnMappings(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "获取现有列名映射失败(将等待下次定时任务重试): %v", err)
		return err
	}
	log.DebugContextf(ctx, "获取到 %d 个现有列名映射", len(existingMappings))

	// 3. 分析现有列名映射，得到所有项目表的列名状态
	projectTableColumnsMap := analyzeAvailableColumns(existingMappings)
	log.DebugContextf(ctx, "分析列名状态完成")

	// 4. 按项目分组字段（同时过滤掉已有映射关系的字段）
	projectGroups := groupFieldsByProject(allFields, existingMappings)
	log.DebugContextf(ctx, "按项目分组完成，共 %d 个项目需要处理", len(projectGroups))

	// 5. 串行处理每个项目
	for _, projectGroup := range projectGroups {
		if err := processProjectColumnMappings(ctx, projectGroup, projectTableColumnsMap, cli); err != nil {
			log.ErrorContextf(ctx, "处理项目 %d 的列名映射失败: %v", projectGroup.ProjectID, err)
			// 继续处理其他项目，不因为单个项目失败而中断整个流程
			continue
		}
		log.InfoContextf(ctx, "项目 %d 的列名映射更新完成", projectGroup.ProjectID)
	}
	log.DebugContextf(ctx, "列名映射更新完成")
	return nil
}

// getAllColumnMappings 获取所有列名映射
func (c *ColumnMappingClient) getAllColumnMappings(ctx context.Context) ([]FieldColumnMapResponse, error) {
	if c.dbDAO == nil {
		return nil, fmt.Errorf("metadata dao is not initialized")
	}

	columns, err := c.dbDAO.GetColumnMapList()
	if err != nil {
		return nil, fmt.Errorf("failed to get column mappings: %v", err)
	}

	mappings := make([]FieldColumnMapResponse, 0, len(columns))
	for _, column := range columns {
		mappings = append(mappings, FieldColumnMapResponse{
			ID:         column.ID,
			FieldID:    column.FieldID,
			ProjectID:  column.ProjectID,
			TableType:  column.TableType,
			ColumnName: column.ColumnName,
		})
	}
	log.DebugContextf(ctx, "Retrieved %d column mappings", len(mappings))
	return mappings, nil
}

// batchAddColumnMappings 批量添加列名映射
func (c *ColumnMappingClient) batchAddColumnMappings(ctx context.Context, mappings []BatchMappingItem) error {
	if len(mappings) == 0 {
		return nil
	}

	if c.dbDAO == nil {
		return fmt.Errorf("metadata dao is not initialized")
	}

	now := time.Now().Format("2006-01-02T15:04:05Z07:00")
	validMappings := make([]metamodel.FieldColumnMap, 0, len(mappings))

	for _, item := range mappings {
		if item.FieldID == 0 || item.ProjectID == 0 || item.TableType == 0 || item.ColumnName == "" {
			log.WarnContextf(ctx, "跳过无效列名映射: %+v", item)
			continue
		}

		exists, err := c.dbDAO.IsColumnMapExists(item.FieldID, item.ProjectID, item.TableType)
		if err != nil {
			return fmt.Errorf("检查列名映射是否存在失败: %v", err)
		}
		if exists {
			log.InfoContextf(ctx, "字段列名映射已存在，跳过添加: fieldID=%d, projectID=%d, tableType=%d, columnName=%s",
				item.FieldID, item.ProjectID, item.TableType, item.ColumnName)
			continue
		}

		validMappings = append(validMappings, metamodel.FieldColumnMap{
			FieldID:    item.FieldID,
			ProjectID:  item.ProjectID,
			TableType:  item.TableType,
			ColumnName: item.ColumnName,
			CreateTime: now,
			ModifyTime: now,
		})
	}

	if len(validMappings) == 0 {
		return nil
	}

	if err := c.dbDAO.BatchAddColumnMaps(validMappings); err != nil {
		return fmt.Errorf("批量添加字段列名映射失败: %v", err)
	}
	log.InfoContextf(ctx, "批量添加 %d 个列名映射成功", len(validMappings))
	return nil
}

// groupFieldsByProject 按项目分组字段，同时过滤掉已有映射关系的字段
func groupFieldsByProject(allFields []*cache.Field, existingMappings []FieldColumnMapResponse) []*ProjectFieldGroups {
	// 构建已有映射的字段ID集合
	existingFieldIDs := make(map[int]bool)
	for _, mapping := range existingMappings {
		existingFieldIDs[mapping.FieldID] = true
	}
	projectMap := make(map[int]*ProjectFieldGroups)

	// 遍历所有字段，按项目ID分组
	for _, field := range allFields {
		// 1. 过滤掉无效字段
		if field.Enabled != constants.EnabledValue {
			continue
		}

		// 2. 过滤掉已经有映射关系的字段
		if existingFieldIDs[field.FieldID] {
			continue
		}

		// 如果项目组不存在，创建新的项目组
		projID := field.ProjID
		if _, exists := projectMap[projID]; !exists {
			projectMap[projID] = &ProjectFieldGroups{
				ProjectID:    projID,
				ObjectFields: make([]*cache.Field, 0),
				DataFields:   make([]*cache.Field, 0),
			}
		}

		// 根据 TableType 分组字段
		if field.TableType == 1 {
			// 对象表字段
			projectMap[projID].ObjectFields = append(projectMap[projID].ObjectFields, field)
		} else {
			// 数据表字段
			projectMap[projID].DataFields = append(projectMap[projID].DataFields, field)
		}
	}

	// 2. 对每个项目内的字段按FieldID升序排序
	for _, group := range projectMap {
		sortFieldsByFieldID(group.ObjectFields)
		sortFieldsByFieldID(group.DataFields)
	}

	// 转换为切片返回
	result := make([]*ProjectFieldGroups, 0, len(projectMap))
	for _, group := range projectMap {
		result = append(result, group)
	}
	return result
}

// analyzeAvailableColumns 分析现有列名映射，得到所有项目表的列名状态
func analyzeAvailableColumns(existingMappings []FieldColumnMapResponse) map[ProjectTableKey]*ProjectTableColumns {
	result := make(map[ProjectTableKey]*ProjectTableColumns)

	// 初始化所有项目表的所有可能列名
	projectTableKeys := make(map[ProjectTableKey]bool)

	// 先收集所有的项目表组合
	for _, mapping := range existingMappings {
		key := ProjectTableKey{
			ProjectID: mapping.ProjectID,
			TableType: mapping.TableType,
		}
		projectTableKeys[key] = true
	}

	// 为每个项目表初始化所有可能的列名
	for key := range projectTableKeys {
		result[key] = initializeProjectTableColumns(key.ProjectID, key.TableType)
	}

	// 标记已使用的列名
	for _, mapping := range existingMappings {
		key := ProjectTableKey{
			ProjectID: mapping.ProjectID,
			TableType: mapping.TableType,
		}

		if projectTableColumns, exists := result[key]; exists {
			if columnStatus, exists := projectTableColumns.Columns[mapping.ColumnName]; exists {
				columnStatus.IsUsed = true
			}
		}
	}
	return result
}

// initializeProjectTableColumns 初始化项目表的所有列名状态
func initializeProjectTableColumns(projectID, tableType int) *ProjectTableColumns {
	projectTableColumns := &ProjectTableColumns{
		ProjectID: projectID,
		TableType: tableType,
		Columns:   make(map[string]*ColumnStatus),
	}

	// 初始化所有支持的列名类型
	columnPrefixes := []string{"c_string", "c_bigint", "c_float", "c_json", "c_time"}
	for _, prefix := range columnPrefixes {
		maxAllowed := getMaxAllowedSuffix(prefix)
		for i := 1; i <= maxAllowed; i++ {
			columnName := fmt.Sprintf("%s_%d", prefix, i)
			projectTableColumns.Columns[columnName] = &ColumnStatus{
				ColumnName: columnName,
				IsUsed:     false,
			}
		}
	}
	return projectTableColumns
}

// getAvailableColumnNames 获取指定前缀的空闲列名（按序号排序）
func getAvailableColumnNames(columnPrefix string, projectTableColumns *ProjectTableColumns) []string {
	var availableColumns []string
	maxAllowed := getMaxAllowedSuffix(columnPrefix)
	for i := 1; i <= maxAllowed; i++ {
		columnName := fmt.Sprintf("%s_%d", columnPrefix, i)
		if columnStatus, exists := projectTableColumns.Columns[columnName]; exists && !columnStatus.IsUsed {
			availableColumns = append(availableColumns, columnName)
		}
	}
	return availableColumns
}

// sortFieldsByFieldID 按FieldID升序排序字段
func sortFieldsByFieldID(fields []*cache.Field) {
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].FieldID < fields[j].FieldID
	})
}

// groupFieldsByType 按字段类型分组字段
func groupFieldsByType(fields []*cache.Field) []*FieldTypeGroup {
	typeMap := make(map[pb.EnumFieldPrimaryFormat][]*cache.Field)

	// 按字段类型分组
	for _, field := range fields {
		fieldType := pb.EnumFieldPrimaryFormat(field.FieldPrimaryFormat)
		typeMap[fieldType] = append(typeMap[fieldType], field)
	}

	// 转换为切片并排序
	var result []*FieldTypeGroup
	for fieldType, typeFields := range typeMap {
		// 每个类型内部按FieldID排序
		sortFieldsByFieldID(typeFields)
		result = append(result, &FieldTypeGroup{
			FieldType: fieldType,
			Fields:    typeFields,
		})
	}

	// 按字段类型排序（保证处理顺序的一致性）
	sort.Slice(result, func(i, j int) bool {
		return result[i].FieldType < result[j].FieldType
	})
	return result
}

// processProjectColumnMappings 项目列名映射处理
func processProjectColumnMappings(ctx context.Context, projectGroup *ProjectFieldGroups,
	projectTableColumnsMap map[ProjectTableKey]*ProjectTableColumns, client *ColumnMappingClient) error {
	projID := projectGroup.ProjectID
	log.InfoContextf(ctx, "开始处理项目 %d 的列名映射，对象表字段: %d 个，数据表字段: %d 个",
		projID, len(projectGroup.ObjectFields), len(projectGroup.DataFields))

	// 收集所有需要新增的映射
	var allNewMappings []BatchMappingItem

	// 处理对象表字段的列名映射
	if len(projectGroup.ObjectFields) > 0 {
		objectMappings, err := assignColumnMappings(ctx, projID,
			projectGroup.ObjectFields, pb.EnumTableType_DATA_OBJECT_TABLE, projectTableColumnsMap)
		if err != nil {
			return fmt.Errorf("分配项目 %d 对象表字段列名映射失败: %v", projID, err)
		}
		allNewMappings = append(allNewMappings, objectMappings...)
		log.InfoContextf(ctx, "项目 %d 对象表字段需要新增 %d 个列名映射", projID, len(objectMappings))
	}

	// 处理数据表字段的列名映射
	if len(projectGroup.DataFields) > 0 {
		dataMappings, err := assignColumnMappings(ctx, projID,
			projectGroup.DataFields, pb.EnumTableType_DATA_TABLE, projectTableColumnsMap)
		if err != nil {
			return fmt.Errorf("分配项目 %d 数据表字段列名映射失败: %v", projID, err)
		}
		allNewMappings = append(allNewMappings, dataMappings...)
		log.InfoContextf(ctx, "项目 %d 数据表字段需要新增 %d 个列名映射", projID, len(dataMappings))
	}

	// 批量添加所有新映射
	if len(allNewMappings) > 0 {
		if err := client.batchAddColumnMappings(ctx, allNewMappings); err != nil {
			return fmt.Errorf("批量添加项目 %d 的列名映射失败: %v", projID, err)
		}
		log.InfoContextf(ctx, "项目 %d 批量添加 %d 个列名映射成功", projID, len(allNewMappings))
	} else {
		log.InfoContextf(ctx, "项目 %d 没有需要新增的列名映射", projID)
	}
	return nil
}

// assignColumnMappings 列名分配函数
func assignColumnMappings(ctx context.Context, projectID int, fields []*cache.Field,
	tableType pb.EnumTableType, projectTableColumnsMap map[ProjectTableKey]*ProjectTableColumns) ([]BatchMappingItem, error) {
	tableTypeStr := "数据表"
	if tableType == pb.EnumTableType_DATA_OBJECT_TABLE {
		tableTypeStr = "对象表"
	}
	tableTypeInt := int(tableType)
	var allNewMappings []BatchMappingItem

	// 按字段类型分组
	fieldTypeGroups := groupFieldsByType(fields)
	log.InfoContextf(ctx, "开始为项目 %d 分配 %s 列名映射", projectID, tableTypeStr)

	projectTableKey := ProjectTableKey{
		ProjectID: projectID,
		TableType: tableTypeInt,
	}

	// 获取该项目表的列名状态
	projectTableColumns, exists := projectTableColumnsMap[projectTableKey]
	if !exists {
		// 如果不存在，创建新的项目表列名状态
		projectTableColumns = initializeProjectTableColumns(projectID, tableTypeInt)
		projectTableColumnsMap[projectTableKey] = projectTableColumns
	}

	// 按字段类型顺序处理
	for _, typeGroup := range fieldTypeGroups {
		// 获取字段类型对应的列名前缀
		columnPrefix, err := getColumnPrefixByFieldType(int(typeGroup.FieldType))
		if err != nil {
			log.ErrorContextf(ctx, "不支持的字段类型 %d: %v", typeGroup.FieldType, err)
			continue
		}

		// 为该类型的字段分配列名
		typeMappings, err := assignColumnNames(ctx, typeGroup.Fields,
			columnPrefix, projectTableColumns)
		if err != nil {
			log.ErrorContextf(ctx, "为项目 %d 字段类型 %s 分配列名失败: %v",
				projectID, columnPrefix, err)
			continue
		}

		allNewMappings = append(allNewMappings, typeMappings...)
		log.InfoContextf(ctx, "项目 %d 字段类型 %s 分配了 %d 个列名映射",
			projectID, columnPrefix, len(typeMappings))
	}
	return allNewMappings, nil
}

// assignColumnNames 按序分配列名给字段
func assignColumnNames(ctx context.Context, fields []*cache.Field,
	columnPrefix string, projectTableColumns *ProjectTableColumns) ([]BatchMappingItem, error) {
	var newMappings []BatchMappingItem

	// 获取该前缀的所有空闲列名（按序号排序）
	availableColumns := getAvailableColumnNames(columnPrefix, projectTableColumns)

	if len(fields) > len(availableColumns) {
		return nil, fmt.Errorf("字段数量 %d 超过可用列名数量 %d (前缀: %s)",
			len(fields), len(availableColumns), columnPrefix)
	}

	// 按FieldID顺序为字段分配列名（字段已经在groupFieldsByType中排序）
	for i, field := range fields {
		if i >= len(availableColumns) {
			break // 防止越界
		}

		columnName := availableColumns[i]
		newMapping := BatchMappingItem{
			FieldID:    field.FieldID,
			ProjectID:  projectTableColumns.ProjectID,
			TableType:  projectTableColumns.TableType,
			ColumnName: columnName,
		}
		newMappings = append(newMappings, newMapping)

		// 标记该列名为已使用
		if columnStatus, exists := projectTableColumns.Columns[columnName]; exists {
			columnStatus.IsUsed = true
		}
		log.DebugContextf(ctx, "为字段 %d (%s) 分配列名: %s",
			field.FieldID, field.FieldName, columnName)
	}
	return newMappings, nil
}

// GetColumnByField 根据字段ID获取对应的列名
func GetColumnByField(fieldID uint32, tableID string) (string, error) {
	// 解析表类型
	tableType := helper.ParseTableType(tableID)

	projectID, err := cache.GetProjectIDByTableID(tableID)
	if err != nil {
		return "", fmt.Errorf("根据tableID查询项目ID失败: %v", err)
	}

	// 从缓存中查询列名
	columnName := cache.GetColumnNameByFieldID(projectID, int(tableType), int(fieldID))
	if columnName == "" {
		return "", fmt.Errorf("未找到字段ID %d 在项目 %d 表类型 %d 的列名映射",
			fieldID, projectID, tableType)
	}
	return columnName, nil
}

// getColumnPrefixByFieldType 根据字段类型获取列名前缀
func getColumnPrefixByFieldType(fieldPrimaryFormat int) (string, error) {
	switch pb.EnumFieldPrimaryFormat(fieldPrimaryFormat) {
	case pb.EnumFieldPrimaryFormat_STRING:
		return "c_string", nil
	case pb.EnumFieldPrimaryFormat_INTEGER:
		return "c_bigint", nil
	case pb.EnumFieldPrimaryFormat_DOUBLE:
		return "c_float", nil
	case pb.EnumFieldPrimaryFormat_TIME:
		return "c_time", nil
	case pb.EnumFieldPrimaryFormat_OPTION:
		return "c_bigint", nil
	case pb.EnumFieldPrimaryFormat_SET:
		return "c_json", nil
	case pb.EnumFieldPrimaryFormat_MAP_KV:
		return "c_json", nil
	case pb.EnumFieldPrimaryFormat_MAP_KLIST:
		return "c_json", nil
	default:
		return "", fmt.Errorf("不支持的字段主要格式: %d", fieldPrimaryFormat)
	}
}

// getMaxAllowedSuffix 获取指定列名前缀的最大允许序号
// 通过全局注册的 schema 提供者动态获取字段数量限制
func getMaxAllowedSuffix(columnPrefix string) int {
	// 获取全局注册的 schema 提供者
	provider := GetSchemaProvider()
	if provider == nil {
		log.Errorf("未找到已注册的 schema 字段限制提供者，字段前缀 %s ", columnPrefix)
		return 0
	}

	// 调用提供者的方法获取字段数量限制
	maxSuffix, err := provider.GetSchemaFieldLimit(columnPrefix)
	if err != nil {
		log.Errorf("从 schema 提供者获取字段前缀 %s 的最大序号失败: %v ", columnPrefix, err)
		return 0
	}
	return maxSuffix
}

// GetFieldByColumn 根据列名获取对应的字段ID
func GetFieldByColumn(columnName string, tableID string) (uint32, error) {
	// 解析表类型
	tableType := helper.ParseTableType(tableID)

	// 使用优化后的项目ID查询函数
	projectID, err := cache.GetProjectIDByTableID(tableID)
	if err != nil {
		return 0, fmt.Errorf("根据tableID查询项目ID失败: %v", err)
	}

	// 从缓存中查询字段ID
	fieldID := cache.GetFieldIDByColumnName(projectID, int(tableType), columnName)
	if fieldID == 0 {
		return 0, fmt.Errorf("未找到列名 %s 在项目 %d 表类型 %d 的字段ID映射", columnName, projectID, tableType)
	}
	return uint32(fieldID), nil
}
