// Package bleve 的公共逻辑
// 物理设备层（即DAO层）的抽象是，给上层提供一张物理表的读写接口。
// 根据物理设备的特性，DAO层接口可以选择支持时序/静态数据的读写，普通字段/map字段的读写。也可以全部支持，也可以部分支持。
// 上层logic层，会根据用户配置，请求DAO层接口实现一个逻辑大宽表的视图。
// 这个逻辑大宽表可能会水平切分成N个物理设备表，或纵向切分成普通字段/map字段物理设备表。切分和路由策略在logic层实现。
// 物理设备层只简单处理单表数据读写。
package bleve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// Bleve Bleve存储对象
type Bleve struct {
	// isGetAll 是否获取所有字段
	isGetAll bool
	// indexPath 索引路径
	indexPath string
	// tableID 表名
	tableID string
	// data 当前字段值
	data map[string]any
	// infos 变更消息队列，key为fieldID
	infos map[uint32]*pb.ModifyFieldInfo
}

// GetDeviceTableID 获取物理设备中的物理表名（一般用于分库分表）
func (b *Bleve) GetDeviceTableID(logicTableID string) string {
	// Bleve的物理表名，不做特殊处理，直接返回逻辑表名
	return logicTableID
}

func (b *Bleve) GetDeviceConn(connectInfo string) error {
	ctx := context.Background()
	log.DebugContextf(ctx, "Bleve 连接信息: %s", connectInfo)

	// 处理connectInfo为localhost的情况，使用配置文件中的路径
	actualConnectInfo := connectInfo
	if connectInfo == "localhost" {
		cfg := config.GetGlobalConfig()
		if cfg != nil && cfg.Bleve.IndexPath != "" {
			actualConnectInfo = cfg.Bleve.IndexPath
			log.InfoContextf(ctx, "connectInfo为localhost，使用配置文件路径: %s", actualConnectInfo)

			// 确保目录存在
			if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
				log.ErrorContextf(ctx, "创建 Bleve 索引目录失败: %v", err)
				return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建Bleve索引目录失败: %v", err))
			}
		} else {
			// 如果配置不可用，使用默认路径
			actualConnectInfo = "../database/bleve"
			log.WarnContextf(ctx, "配置不可用，使用默认 Bleve 路径: %s", actualConnectInfo)

			// 确保目录存在
			if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
				log.ErrorContextf(ctx, "创建 Bleve 索引目录失败: %v", err)
				return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建Bleve索引目录失败: %v", err))
			}
		}
	}
	if absPath, err := filepath.Abs(actualConnectInfo); err == nil {
		actualConnectInfo = absPath
	}

	// 设置索引路径
	b.indexPath = actualConnectInfo
	log.InfoContextf(ctx, "Bleve 索引路径设置成功: %s", actualConnectInfo)
	return nil
}

// CloseDeviceConn 关闭Bleve索引连接
func (b *Bleve) CloseDeviceConn() error {
	// 由于不再有实例级别的index字段，这个方法现在只是清理状态
	b.indexPath = ""
	b.tableID = ""
	return nil
}

// GetDeviceKey 返回Bleve实例名称或连接标识
func (b *Bleve) GetDeviceKey() string {
	return "bleve"
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

func (b *Bleve) documentToDocRow(fields map[string]any) *pb.DocRow {
	docRow := &pb.DocRow{
		Fields: make(map[uint32]*pb.FieldInfo),
	}

	// 从系统字段中获取rowID和times
	b.parseSystemFields(fields, docRow)

	// 处理字段数据
	b.processDocumentFields(fields, docRow)
	return docRow
}

// buildQuery 构建包含"未删除"条件的查询
func (b *Bleve) buildQuery(baseQuery query.Query) query.Query {
	// 创建未删除条件：_deleted字段不等于"1"或者_deleted字段不存在
	notDeletedQuery := bleve.NewBooleanQuery()

	// 条件1：_deleted字段不存在或不等于"1"
	deletedFieldExistsQuery := bleve.NewTermQuery("1")
	deletedFieldExistsQuery.SetField("_deleted")
	notDeletedQuery.AddMustNot(deletedFieldExistsQuery)

	// 如果baseQuery是MatchAllQuery，直接返回未删除条件
	if _, ok := baseQuery.(*query.MatchAllQuery); ok {
		return notDeletedQuery
	}

	// 否则，组合baseQuery和未删除条件
	conjunctionQuery := bleve.NewConjunctionQuery(baseQuery, notDeletedQuery)
	return conjunctionQuery
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// 系统字段列表
var (
	systemFields = map[string]bool{
		"_row_id":            true,
		"_ctime":             true,
		"_mtime":             true,
		"_replay_timestamps": true,
		"_times":             true,
	}
)

// executeBatch 执行批处理操作
func (b *Bleve) executeBatch(ctx context.Context, batch *bleve.Batch, index bleve.Index) error {
	if batch == nil || batch.Size() == 0 {
		return nil
	}

	err := index.Batch(batch)
	if err != nil {
		log.WarnContextf(ctx, "批处理执行失败: %v", err)
		return fmt.Errorf("批处理执行失败: %v", err)
	}
	return nil
}

// parseSystemFields 从系统字段中获取rowID和times
func (b *Bleve) parseSystemFields(fields map[string]any, docRow *pb.DocRow) {
	// 从 _row_id 字段获取 rowID
	if rowID, exists := fields["_row_id"]; exists {
		if rowIDStr, ok := rowID.(string); ok {
			docRow.RowId = rowIDStr
		}
	}

	// 从 _times 字段获取 times，并确保格式一致
	if times, exists := fields["_times"]; exists {
		if timesStr, ok := times.(string); ok {
			// 统一转换为 "YYYY-MM-DD HH:MM:SS" 格式
			docRow.Times = utils.NormalizeTimeString(timesStr)
		}
	}
}

// processDocumentFields 处理文档字段数据
func (b *Bleve) processDocumentFields(fields map[string]interface{}, docRow *pb.DocRow) {
	ctx := context.Background()
	for key, value := range fields {
		// 跳过系统字段
		if systemFields[key] {
			continue
		}

		// 转换fieldID
		fieldID, err := strconv.ParseUint(key, 10, 32)
		if err != nil {
			continue
		}

		// 获取字段类型
		fieldType, err := b.getFieldType(ctx, uint32(fieldID))
		if err != nil {
			log.WarnContextf(ctx, "Failed to get field type for fieldID %d: %v", fieldID, err)
			continue
		}

		// 根据字段类型进行值转换，确保格式一致
		convertedValue := b.convertValueByFieldType(value, fieldType)

		// 创建字段信息
		fieldInfo := b.createFieldInfoWithType(convertedValue, fieldType)
		docRow.Fields[uint32(fieldID)] = fieldInfo
	}
}

// createFieldInfoWithType 根据字段类型创建FieldInfo
func (b *Bleve) createFieldInfoWithType(value interface{}, fieldType pb.EnumFieldType) *pb.FieldInfo {
	ctx := context.Background()
	if value == nil {
		return &pb.FieldInfo{
			FieldType: fieldType,
		}
	}

	// 根据字段类型进行类型转换
	switch fieldType {
	case pb.EnumFieldType_STR_FIELD:
		return b.createStringFieldInfo(b.processStrValue(ctx, value))
	case pb.EnumFieldType_INT_FIELD:
		return b.createIntFieldInfoFromValue(b.processIntValue(ctx, value))
	case pb.EnumFieldType_FLOAT_FIELD:
		return b.createFloatFieldInfoFromValue(b.processFloatValue(ctx, value))
	case pb.EnumFieldType_TIME_FIELD:
		return b.createTimeFieldInfo(b.processTimeValue(ctx, value))
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		if mapVal, ok := value.(map[string]interface{}); ok {
			return b.createMapFieldInfo(mapVal)
		}
		return b.createDefaultFieldInfo(value)
	default:
		return b.createDefaultFieldInfo(value)
	}
}

// createStringFieldInfo 创建字符串类型字段信息
func (b *Bleve) createStringFieldInfo(value string) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_STR_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{
				Str: value,
			},
		},
	}
}

// createMapFieldInfo 创建映射类型字段信息
func (b *Bleve) createMapFieldInfo(value map[string]interface{}) *pb.FieldInfo {
	mapContainer := &pb.MapContainer{
		Entries: make(map[string]*pb.KeyValueEntry),
	}

	for k, mv := range value {
		entry := b.createKeyValueEntry(mv)
		mapContainer.Entries[k] = entry
	}

	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_MAP_KV_FIELD,
		MapValue:  mapContainer,
	}
}

// createKeyValueEntry 创建键值对条目
func (b *Bleve) createKeyValueEntry(value interface{}) *pb.KeyValueEntry {
	entry := &pb.KeyValueEntry{}

	switch mvt := value.(type) {
	case string:
		entry.Type = pb.EnumFieldType_STR_FIELD
		entry.Value = &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{
				Str: mvt,
			},
		}
	case float64:
		entry.Type = pb.EnumFieldType_FLOAT_FIELD
		entry.Value = &pb.SimpleValue{
			Value: &pb.SimpleValue_Float{
				Float: mvt,
			},
		}
	case int, int32, int64:
		entry.Type = pb.EnumFieldType_INT_FIELD
		var intVal int64
		switch v := mvt.(type) {
		case int:
			intVal = int64(v)
		case int32:
			intVal = int64(v)
		case int64:
			intVal = v
		}
		entry.Value = &pb.SimpleValue{
			Value: &pb.SimpleValue_Int{
				Int: intVal,
			},
		}
	case bool:
		entry.Type = pb.EnumFieldType_STR_FIELD
		entry.Value = &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{
				Str: fmt.Sprintf("%v", mvt),
			},
		}
	}

	return entry
}

// createDefaultFieldInfo 创建默认类型字段信息
func (b *Bleve) createDefaultFieldInfo(value interface{}) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_STR_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{
				Str: fmt.Sprintf("%v", value),
			},
		},
	}
}

// 类型转换方法 - 仿照DuckDB实现
// processStrValue 处理字符串类型值
func (b *Bleve) processStrValue(_ context.Context, value any) string {
	if strVal, ok := value.(string); ok {
		return strVal
	} else if bVal, ok := value.([]byte); ok {
		return string(bVal)
	} else {
		return fmt.Sprintf("%v", value)
	}
}

// processIntValue 处理整型值
func (b *Bleve) processIntValue(ctx context.Context, value any) int64 {
	var intVal int64
	if i64, ok := value.(int64); ok {
		intVal = i64
	} else if i32, ok := value.(int32); ok {
		intVal = int64(i32)
	} else if i, ok := value.(int); ok {
		intVal = int64(i)
	} else if u64, ok := value.(uint64); ok {
		intVal = int64(u64)
	} else if u32, ok := value.(uint32); ok {
		intVal = int64(u32)
	} else if strVal, ok := value.(string); ok {
		if v, err := strconv.ParseInt(strVal, 10, 64); err == nil {
			intVal = v
		}
	} else if bVal, ok := value.([]byte); ok {
		if v, err := strconv.ParseInt(string(bVal), 10, 64); err == nil {
			intVal = v
		}
	} else if f64, ok := value.(float64); ok {
		// 处理浮点数到整数的转换
		intVal = int64(f64)
	} else {
		log.WarnContextf(ctx, "Failed to parse INT_FIELD type: %T", value)
	}
	return intVal
}

// processFloatValue 处理浮点型值
func (b *Bleve) processFloatValue(ctx context.Context, value any) float64 {
	var floatVal float64
	if f64, ok := value.(float64); ok {
		floatVal = f64
	} else if f32, ok := value.(float32); ok {
		floatVal = float64(f32)
	} else if strVal, ok := value.(string); ok {
		if v, err := strconv.ParseFloat(strVal, 64); err == nil {
			floatVal = v
		}
	} else if bVal, ok := value.([]byte); ok {
		if v, err := strconv.ParseFloat(string(bVal), 64); err == nil {
			floatVal = v
		}
	} else if i64, ok := value.(int64); ok {
		// 处理整数到浮点数的转换
		floatVal = float64(i64)
	} else if i, ok := value.(int); ok {
		floatVal = float64(i)
	} else {
		log.WarnContextf(ctx, "Failed to parse FLOAT_FIELD type: %T", value)
	}
	return floatVal
}

// processTimeValue 处理时间值
func (b *Bleve) processTimeValue(ctx context.Context, value any) string {
	var timeStr string
	if strVal, ok := value.(string); ok {
		timeStr = strVal
	} else if bVal, ok := value.([]byte); ok {
		timeStr = string(bVal)
	} else {
		log.WarnContextf(ctx, "Failed to parse TIME_FIELD type: %T", value)
		timeStr = fmt.Sprintf("%v", value)
	}
	return timeStr
}

// 新的字段信息创建方法
// createIntFieldInfoFromValue 根据转换后的整数值创建整数字段信息
func (b *Bleve) createIntFieldInfoFromValue(intVal int64) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_INT_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Int{
				Int: intVal,
			},
		},
	}
}

// createFloatFieldInfoFromValue 根据转换后的浮点数值创建浮点数字段信息
func (b *Bleve) createFloatFieldInfoFromValue(floatVal float64) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_FLOAT_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Float{
				Float: floatVal,
			},
		},
	}
}

// createTimeFieldInfo 创建时间字段信息
func (b *Bleve) createTimeFieldInfo(timeStr string) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_TIME_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Time{
				Time: timeStr,
			},
		},
	}
}

// getTableIndexPath 获取表对应的索引路径
func (b *Bleve) getTableIndexPath(tableID string) string {
	return filepath.Join(b.indexPath, tableID+".bleve")
}

// fieldID2ColName 将字段ID转换为列名
func (b *Bleve) fieldID2ColName(fieldID uint64, mapKey string) string {
	colName := strconv.FormatUint(fieldID, 10)
	if mapKey != "" {
		colName = fmt.Sprintf("%s.%s", colName, mapKey)
	}
	return colName
}

// getFieldType 根据字段ID获取字段类型
func (b *Bleve) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return pb.EnumFieldType_INVALID_FIELD, fmt.Errorf("field info not found in cache for fieldID: %d", fieldID)
	}
	return pb.EnumFieldType(field.FieldPrimaryFormat), nil // 字段一级格式，即字段存储类型
}

// convertValueByFieldType 根据字段类型转换值，确保格式一致
func (b *Bleve) convertValueByFieldType(value interface{}, fieldType pb.EnumFieldType) interface{} {
	if value == nil {
		return value
	}

	switch fieldType {
	case pb.EnumFieldType_INT_FIELD:
		// 整型字段：确保是 int64
		switch v := value.(type) {
		case string:
			if iv, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return iv
			}
		case float64:
			return int64(v)
		case int:
			return int64(v)
		case int32:
			return int64(v)
		case int64:
			return v
		}
	case pb.EnumFieldType_FLOAT_FIELD:
		// 浮点字段：确保是 float64
		switch v := value.(type) {
		case string:
			if fv, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return fv
			}
		case int64:
			return float64(v)
		case int:
			return float64(v)
		case float32:
			return float64(v)
		case float64:
			return v
		}
	case pb.EnumFieldType_TIME_FIELD:
		// 时间字段：统一格式为 "YYYY-MM-DD HH:MM:SS"
		if timeStr, ok := value.(string); ok {
			return utils.NormalizeTimeString(timeStr)
		}
	}

	// 其他类型直接返回
	return value
}

// buildIndexMapping 基于字段缓存构建索引映射（数值/时间/字符串）
func buildIndexMapping() *mapping.IndexMappingImpl {
	// 顶层索引映射
	indexMapping := bleve.NewIndexMapping()
	// 文档默认映射
	docMapping := bleve.NewDocumentMapping()
	indexMapping.DefaultMapping = docMapping

	// 系统字段映射
	keyword := bleve.NewTextFieldMapping()
	keyword.Analyzer = "keyword"
	keyword.Store = true
	keyword.Index = true
	datetime := bleve.NewDateTimeFieldMapping()
	datetime.Store = true
	datetime.Index = true

	docMapping.AddFieldMappingsAt("_row_id", keyword)
	docMapping.AddFieldMappingsAt("_deleted", keyword)
	docMapping.AddFieldMappingsAt("_ctime", datetime)
	docMapping.AddFieldMappingsAt("_mtime", datetime)
	docMapping.AddFieldMappingsAt("_times", datetime)

	// 用户字段映射（根据全量字段缓存）
	fields := cache.GetAllFieldInfo()
	var addCount int
	for _, f := range fields {
		fieldName := strconv.Itoa(f.FieldID)
		switch pb.EnumFieldType(f.FieldPrimaryFormat) {
		case pb.EnumFieldType_STR_FIELD:
			fm := bleve.NewTextFieldMapping()
			fm.Analyzer = "keyword"
			fm.Store = true
			fm.Index = true
			docMapping.AddFieldMappingsAt(fieldName, fm)
			addCount++
		case pb.EnumFieldType_INT_FIELD, pb.EnumFieldType_FLOAT_FIELD:
			fm := bleve.NewNumericFieldMapping()
			fm.Store = true
			fm.Index = true
			docMapping.AddFieldMappingsAt(fieldName, fm)
			addCount++
		case pb.EnumFieldType_TIME_FIELD:
			fm := bleve.NewDateTimeFieldMapping()
			fm.Store = true
			fm.Index = true
			docMapping.AddFieldMappingsAt(fieldName, fm)
			addCount++
		default:
			// 其他类型（如MAP），暂不显式映射，由动态映射处理
		}
	}
	log.InfoContextf(context.Background(), "构建Bleve索引映射完成: 字段映射数量=%d", addCount)
	return indexMapping
}

// getIndex 获取Bleve索引，每次都重新打开
func getIndex(ctx context.Context, connectInfo string) (bleve.Index, error) {
	log.InfoContextf(ctx, "Bleve 索引路径: %s", connectInfo)

	// 尝试打开已存在的索引
	log.InfoContextf(ctx, "尝试打开Bleve索引: %s", connectInfo)
	index, err := bleve.Open(connectInfo)
	if err != nil {
		// 如果索引不存在，则创建新索引（使用字段映射）
		log.InfoContextf(ctx, "索引不存在，创建新的Bleve索引: %s", connectInfo)
		indexMapping := buildIndexMapping()
		index, err = bleve.New(connectInfo, indexMapping)
		if err != nil {
			log.ErrorContextf(ctx, "创建 Bleve 索引失败: %v", err)
			return nil, errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建 Bleve 索引失败: %v", err))
		}
		log.InfoContextf(ctx, "成功创建新的Bleve索引: %s", connectInfo)
	} else {
		log.InfoContextf(ctx, "成功打开已存在的Bleve索引: %s", connectInfo)
	}
	return index, nil
}
