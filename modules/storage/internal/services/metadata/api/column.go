package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// FieldColumnMap 字段-DB列名映射表结构
type FieldColumnMap struct {
	model.FieldColumnMap
	dbDAO dao.DataInterfacer
}

var NewFieldColumnMap = func() SchemaHandler {
	var imp FieldColumnMap
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewFieldColumnMap NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterColumnHandler 注册字段列名映射处理器到API入口
func RegisterColumnHandler() {
	// 注册字段列名映射表处理器
	GetAPIHandleInstance().Register(NewFieldColumnMap())
}

// SchemaID 获取schemaID
func (f *FieldColumnMap) SchemaID() string {
	return model.FieldColumnMapTableName
}

// GetHandle http-Get请求（读数据）
func (f *FieldColumnMap) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", f.SchemaID(), params)

	// 根据不同的查询参数执行不同的查询逻辑
	fieldID := params["field_id"]
	projectID := params["project_id"]
	tableType := params["table_type"]
	columnName := params["column_name"]

	var result interface{}
	var err error

	switch {
	case fieldID != "" && projectID != "" && tableType != "":
		// 根据字段ID、项目ID和表类型查询特定映射
		fieldIDInt := parseIntParam(fieldID, 0)
		projectIDInt := parseIntParam(projectID, 0)
		tableTypeInt := parseIntParam(tableType, 0)
		result, err = f.dbDAO.GetColumnMapByFieldProjectAndType(fieldIDInt, projectIDInt, tableTypeInt)
		if err != nil {
			log.Errorf("GetColumnMapByFieldProjectAndType failed: %v", err)
			return NewAPIErrorRsp(501, "查询字段列名映射失败"), err
		}

	case projectID != "" && tableType != "" && columnName != "":
		// 根据项目ID、表类型和列名查询字段ID列表
		projectIDInt := parseIntParam(projectID, 0)
		tableTypeInt := parseIntParam(tableType, 0)
		result, err = f.dbDAO.GetFieldIDsByProjectTypeAndColumn(projectIDInt, tableTypeInt, columnName)
		if err != nil {
			log.Errorf("GetFieldIDsByProjectTypeAndColumn failed: %v", err)
			return NewAPIErrorRsp(504, "查询字段ID失败"), err
		}

	case fieldID != "":
		// 根据字段ID查询所有相关映射
		fieldIDInt := parseIntParam(fieldID, 0)
		result, err = f.dbDAO.GetColumnMapByFieldID(fieldIDInt)
		if err != nil {
			log.Errorf("GetColumnMapByFieldID failed: %v", err)
			return NewAPIErrorRsp(502, "查询字段列名映射失败"), err
		}

	case projectID != "" && tableType != "":
		// 根据项目ID和表类型查询所有相关映射
		projectIDInt := parseIntParam(projectID, 0)
		tableTypeInt := parseIntParam(tableType, 0)
		result, err = f.dbDAO.GetColumnMapByProjectAndType(projectIDInt, tableTypeInt)
		if err != nil {
			log.Errorf("GetColumnMapByProjectAndType failed: %v", err)
			return NewAPIErrorRsp(503, "查询字段列名映射失败"), err
		}

	case projectID != "":
		// 根据项目ID查询所有相关映射
		projectIDInt := parseIntParam(projectID, 0)
		result, err = f.dbDAO.GetColumnMapByProjectID(projectIDInt)
		if err != nil {
			log.Errorf("GetColumnMapByProjectID failed: %v", err)
			return NewAPIErrorRsp(503, "查询字段列名映射失败"), err
		}

	default:
		// 查询所有映射
		result, err = f.dbDAO.GetColumnMapList()
		if err != nil {
			log.Errorf("GetColumnMapList failed: %v", err)
			return NewAPIErrorRsp(505, "查询字段列名映射列表失败"), err
		}
	}
	return NewAPISuccessRsp(result), nil
}

// PostHandle http-Post请求（写数据）
func (f *FieldColumnMap) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.Debugf("FieldColumnMap PostHandle params: %+v", params)
	action := params["action"]
	if action == "" {
		action = "add" // 默认为添加操作
	}

	switch action {
	case "add":
		return f.handleAdd(ctx, params)
	case "update":
		return f.handleUpdate(ctx, params)
	case "delete":
		return f.handleDelete(ctx, params)
	case "batch_add":
		return f.handleBatchAdd(ctx, params)
	default:
		return NewAPIErrorRsp(401, "不支持的操作类型: "+action), nil
	}
}

// handleAdd 处理添加操作
func (f *FieldColumnMap) handleAdd(ctx context.Context, params map[string]string) (*APIRsp, error) {
	fieldID := parseIntParam(params["field_id"], 0)
	projectID := parseIntParam(params["project_id"], 0)
	tableType := parseIntParam(params["table_type"], 0)
	columnName := params["column_name"]
	if fieldID == 0 || projectID == 0 || tableType == 0 || columnName == "" {
		return NewAPIErrorRsp(402, "字段ID、项目ID、表类型和列名不能为空"), nil
	}

	// 检查映射是否已存在
	exists, err := f.dbDAO.IsColumnMapExists(fieldID, projectID, tableType)
	if err != nil {
		log.Errorf("IsColumnMapExists failed: %v", err)
		return NewAPIErrorRsp(500, "检查映射是否存在失败"), err
	}
	if exists {
		log.InfoContextf(ctx, "字段列名映射已存在，跳过添加: fieldID=%d, projectID=%d, tableType=%d, columnName=%s",
			fieldID, projectID, tableType, columnName)
		return NewAPIErrorRsp(200, "success"), nil
	}

	currentTime := time.Now().Format("2006-01-02T15:04:05Z07:00")
	columnMap := &model.FieldColumnMap{
		FieldID:    fieldID,
		ProjectID:  projectID,
		TableType:  tableType,
		ColumnName: columnName,
		CreateTime: currentTime,
		ModifyTime: currentTime,
	}
	err = f.dbDAO.AddColumnMap(columnMap)
	if err != nil {
		log.Errorf("AddColumnMap failed: %v", err)
		return NewAPIErrorRsp(510, "添加字段列名映射失败"), err
	}
	return NewAPISuccessRsp(columnMap), nil
}

// handleUpdate 处理更新操作
func (f *FieldColumnMap) handleUpdate(ctx context.Context, params map[string]string) (*APIRsp, error) {
	id := parseIntParam(params["id"], 0)
	fieldID := parseIntParam(params["field_id"], 0)
	projectID := parseIntParam(params["project_id"], 0)
	tableType := parseIntParam(params["table_type"], 0)
	columnName := params["column_name"]
	if id == 0 {
		return NewAPIErrorRsp(403, "ID不能为空"), nil
	}
	currentTime := time.Now().Format("2006-01-02T15:04:05Z07:00")
	columnMap := &model.FieldColumnMap{
		ID:         id,
		ModifyTime: currentTime,
	}

	if fieldID > 0 {
		columnMap.FieldID = fieldID
	}
	if projectID > 0 {
		columnMap.ProjectID = projectID
	}
	if tableType > 0 {
		columnMap.TableType = tableType
	}
	if columnName != "" {
		columnMap.ColumnName = columnName
	}
	err := f.dbDAO.UpdateColumnMap(columnMap)
	if err != nil {
		log.Errorf("UpdateColumnMap failed: %v", err)
		return NewAPIErrorRsp(500, "更新字段列名映射失败"), err
	}
	return NewAPISuccessRsp(columnMap), nil
}

// handleDelete 处理删除操作
func (f *FieldColumnMap) handleDelete(ctx context.Context, params map[string]string) (*APIRsp, error) {
	id := parseIntParam(params["id"], 0)
	fieldID := parseIntParam(params["field_id"], 0)
	projectID := parseIntParam(params["project_id"], 0)
	tableType := parseIntParam(params["table_type"], 0)

	var err error
	var message string

	switch {
	case id > 0:
		// 根据ID删除
		err = f.dbDAO.DeleteColumnMap(id)
		message = "删除字段列名映射成功"
	case fieldID > 0:
		// 根据字段ID删除所有相关映射
		err = f.dbDAO.DeleteColumnMapByFieldID(fieldID)
		message = "删除字段相关的所有列名映射成功"
	case projectID > 0 && tableType > 0:
		// 根据项目ID和表类型删除所有相关映射
		err = f.dbDAO.DeleteColumnMapByProjectAndType(projectID, tableType)
		message = "删除项目和表类型相关的所有列名映射成功"
	case projectID > 0:
		// 根据项目ID删除所有相关映射
		err = f.dbDAO.DeleteColumnMapByProjectID(projectID)
		message = "删除项目相关的所有列名映射成功"
	default:
		return NewAPIErrorRsp(405, "删除参数不能为空"), nil
	}

	if err != nil {
		log.Errorf("Delete operation failed: %v", err)
		return NewAPIErrorRsp(500, "删除字段列名映射失败"), err
	}

	return NewAPISuccessRsp(map[string]string{"message": message}), nil
}

// BatchMappingItem 批量映射项结构
type BatchMappingItem struct {
	FieldID    int    `json:"field_id"`
	ProjectID  int    `json:"project_id"`
	TableType  int    `json:"table_type"`
	ColumnName string `json:"column_name"`
}

// handleBatchAdd 处理批量添加操作
func (f *FieldColumnMap) handleBatchAdd(ctx context.Context, params map[string]string) (*APIRsp, error) {
	mappingsJSON := params["mappings"]
	if mappingsJSON == "" {
		return NewAPIErrorRsp(402, "批量映射数据不能为空"), nil
	}

	// 解析JSON数据
	var batchItems []BatchMappingItem
	if err := json.Unmarshal([]byte(mappingsJSON), &batchItems); err != nil {
		log.Errorf("解析批量映射JSON失败: %v", err)
		return NewAPIErrorRsp(403, "批量映射数据格式错误"), nil
	}

	if len(batchItems) == 0 {
		return NewAPIErrorRsp(404, "批量映射数据为空"), nil
	}

	// 验证和准备数据
	var validMappings []model.FieldColumnMap
	var skippedCount int
	var errorMessages []string

	for i, item := range batchItems {
		// 验证必填字段
		if item.FieldID == 0 || item.ProjectID == 0 || item.TableType == 0 || item.ColumnName == "" {
			errorMessages = append(errorMessages,
				fmt.Sprintf("第%d项: 字段ID、项目ID、表类型和列名不能为空", i+1))
			continue
		}

		// 检查映射是否已存在
		exists, err := f.dbDAO.IsColumnMapExists(item.FieldID, item.ProjectID, item.TableType)
		if err != nil {
			log.Errorf("检查映射是否存在失败: %v", err)
			errorMessages = append(errorMessages,
				fmt.Sprintf("第%d项: 检查映射是否存在失败", i+1))
			continue
		}
		if exists {
			log.InfoContextf(ctx, "字段列名映射已存在，跳过添加: fieldID=%d, projectID=%d, tableType=%d, columnName=%s",
				item.FieldID, item.ProjectID, item.TableType, item.ColumnName)
			skippedCount++
			continue
		}

		// 添加到有效映射列表
		currentTime := time.Now().Format("2006-01-02T15:04:05Z07:00")
		validMappings = append(validMappings, model.FieldColumnMap{
			FieldID:    item.FieldID,
			ProjectID:  item.ProjectID,
			TableType:  item.TableType,
			ColumnName: item.ColumnName,
			CreateTime: currentTime,
			ModifyTime: currentTime,
		})
	}

	// 如果有验证错误，返回错误信息
	if len(errorMessages) > 0 {
		return NewAPIErrorRsp(405, fmt.Sprintf("验证失败: %v", errorMessages)), nil
	}

	// 执行批量添加
	if len(validMappings) > 0 {
		if err := f.dbDAO.BatchAddColumnMaps(validMappings); err != nil {
			log.Errorf("批量添加字段列名映射失败: %v", err)
			return NewAPIErrorRsp(510, "批量添加字段列名映射失败"), err
		}
	}

	// 构建响应数据
	result := map[string]interface{}{
		"total_count":   len(batchItems),
		"added_count":   len(validMappings),
		"skipped_count": skippedCount,
		"message": fmt.Sprintf("批量添加完成: 总计%d项，新增%d项，跳过%d项",
			len(batchItems), len(validMappings), skippedCount),
	}
	return NewAPISuccessRsp(result), nil
}

// parseIntParam 解析字符串参数为整数，如果解析失败则返回默认值
func parseIntParam(param string, defaultValue int) int {
	if param == "" {
		return defaultValue
	}
	if val, err := strconv.Atoi(param); err == nil {
		return val
	}
	return defaultValue
}

// NewAPISuccessRsp 创建成功响应
func NewAPISuccessRsp(data interface{}) *APIRsp {
	// 检查数据类型，如果已经是切片则直接使用，否则包装成切片
	var dataList []any
	if data != nil {
		// 检查是否为model.FieldColumnMap切片
		if columnMaps, ok := data.([]model.FieldColumnMap); ok {
			dataList = make([]any, len(columnMaps))
			for i, cm := range columnMaps {
				dataList[i] = cm
			}
		} else if columnMap, ok := data.(*model.FieldColumnMap); ok {
			// 单个FieldColumnMap对象
			dataList = []any{*columnMap}
		} else if intSlice, ok := data.([]int); ok {
			// 整数切片（如字段ID列表）
			dataList = make([]any, len(intSlice))
			for i, v := range intSlice {
				dataList[i] = v
			}
		} else if slice, ok := data.([]any); ok {
			dataList = slice
		} else {
			dataList = []any{data}
		}
	} else {
		dataList = []any{}
	}
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}
}

// NewAPIErrorRsp 创建错误响应
func NewAPIErrorRsp(code int, message string) *APIRsp {
	return &APIRsp{
		Code: code,
		Data: []any{map[string]string{"error": message}},
	}
}
