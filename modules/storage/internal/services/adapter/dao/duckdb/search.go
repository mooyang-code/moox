package duckdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SearchFieldInfos 统一搜索接口，支持静态数据和时序数据
func (d *DuckDB) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "&&&&&&&&&& DuckDB SearchFieldInfos:%+v", params)
	d.tableID = params.TableID
	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return d.SearchTimingFieldInfos(ctx, params.TableID, params.TimeInterval, params.TimeSort, params.SearchOptions, params.PageInfo)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return d.SearchStaticFieldInfos(ctx, params.TableID, params.RowID, params.SearchOptions, params.PageInfo)
	default:
		// 处理默认情况
		return nil, 0, fmt.Errorf("invalid data type")
	}
}

// SearchStaticFieldInfos 非时序数据：搜索接口
func (d *DuckDB) SearchStaticFieldInfos(ctx context.Context, tableID string, rowID string,
	searchOp *pb.SearchOptions, pageInfo *pb.PageInfo) ([]*pb.DocRow, uint64, error) {
	// 构建SQL查询（静态数据使用默认降序排序）
	params := &searchQueryParams{
		tableID:      tableID,
		rowID:        rowID,
		timeInterval: nil,
		timeSort:     pb.Sort_Desc,
		searchOp:     searchOp,
		pageInfo:     pageInfo,
	}
	query, args, err := d.buildSearchQuery(ctx, params)
	if err != nil {
		return nil, 0, err
	}

	// 执行查询
	return d.executeSearchQuery(ctx, query, args)
}

// SearchTimingFieldInfos 时序数据：搜索接口（支持时序排序）
func (d *DuckDB) SearchTimingFieldInfos(ctx context.Context, tableID string,
	timeInterval *pb.TimeInterval, timeSort pb.Sort, searchOp *pb.SearchOptions, pageInfo *pb.PageInfo) ([]*pb.DocRow, uint64, error) {
	// 构建SQL查询（直接传递timeInterval参数）
	params := &searchQueryParams{
		tableID:      tableID,
		rowID:        "",
		timeInterval: timeInterval,
		timeSort:     timeSort,
		searchOp:     searchOp,
		pageInfo:     pageInfo,
	}
	query, args, err := d.buildSearchQuery(ctx, params)
	if err != nil {
		return nil, 0, err
	}

	// 执行查询
	return d.executeSearchQuery(ctx, query, args)
}

// searchQueryParams 搜索查询参数
type searchQueryParams struct {
	tableID      string
	rowID        string
	timeInterval *pb.TimeInterval
	timeSort     pb.Sort
	searchOp     *pb.SearchOptions
	pageInfo     *pb.PageInfo
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

// executeSearchQuery 执行搜索查询
func (d *DuckDB) executeSearchQuery(ctx context.Context, query string, args []any) ([]*pb.DocRow, uint64, error) {
	// 构建计数查询语句，去掉ORDER BY, LIMIT和OFFSET部分
	countQuery := buildCountQuery(query)

	// 执行计数查询
	var totalCount uint64
	err := d.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount)
	if err != nil {
		// 检查是否是表不存在的错误
		if d.isTableNotExistError(err) {
			log.WarnContextf(ctx, "Table does not exist, returning empty result: %v", err)
			return []*pb.DocRow{}, 0, nil
		}
		log.ErrorContextf(ctx, "Failed to execute count query: %v, SQL: %s", err, countQuery)
		return nil, 0, fmt.Errorf("failed to execute count query: %v", err)
	}

	// 执行数据查询
	results, err := d.executeQueryWithRowProcessor(ctx, query, args, nil,
		func(docRow *pb.DocRow, columns []string, values []any) {
			// 处理row_id和times
			for i, col := range columns {
				if col == "_row_id" {
					if rowIDVal, ok := values[i].(string); ok {
						docRow.RowId = rowIDVal
					}
				} else if col == "_times" {
					if timesVal, ok := values[i].(string); ok {
						docRow.Times = timesVal
					} else if timeVal, ok := values[i].(time.Time); ok {
						// 处理time.Time类型，转换为字符串格式
						docRow.Times = timeVal.Format("2006-01-02 15:04:05")
					} else {
						// 处理其他类型，尝试转换为字符串
						docRow.Times = fmt.Sprintf("%v", values[i])
					}
				}
			}
		})
	return results, totalCount, err
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// buildSearchQuery 构建搜索查询（支持时序排序）
func (d *DuckDB) buildSearchQuery(ctx context.Context, params *searchQueryParams) (string, []any, error) {
	var conditions []string
	var args []any

	// 添加行ID条件（如果有）
	if params.rowID != "" {
		conditions = append(conditions, "_row_id = ?")
		args = append(args, params.rowID)
	}

	// 添加时间条件（如果有）
	if params.timeInterval != nil {
		timeCondition, timeArgs := d.buildTimeIntervalCond(params.timeInterval)
		if timeCondition != "" {
			conditions = append(conditions, timeCondition)
			args = append(args, timeArgs...)
		}
	}

	// 添加未删除条件
	conditions = append(conditions, d.buildNotDeletedCondition())

	// 处理搜索条件组
	if len(params.searchOp.CondGroups) > 0 {
		condGroupsSQL, condGroupsArgs, err := d.buildCondGroupsSQL(ctx, params.searchOp.CondGroups, params.searchOp.Logical)
		if err != nil {
			return "", nil, err
		}

		if condGroupsSQL != "" {
			conditions = append(conditions, condGroupsSQL)
			args = append(args, condGroupsArgs...)
		}
	}

	// 构建WHERE子句
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// 构建ORDER BY子句（优先使用时序排序）
	orderByClause := d.buildOrderByClause(ctx, params.timeSort, params.searchOp.Sort)

	// 构建LIMIT和OFFSET子句
	limitOffset := d.buildLimitOffsetClause(params.searchOp.MaxNum, params.pageInfo)

	// 构建完整的SQL查询
	query := fmt.Sprintf("SELECT * FROM %s %s %s %s",
		params.tableID,
		whereClause,
		orderByClause,
		limitOffset)
	return query, args, nil
}

// buildCondGroupsSQL 构建条件组SQL
func (d *DuckDB) buildCondGroupsSQL(ctx context.Context, condGroups []*pb.SearchCondGroup,
	logical pb.Logical) (string, []any, error) {
	if len(condGroups) == 0 {
		return "", nil, nil
	}

	// 处理每个条件组
	var groupConditions []string
	var args []any
	for _, group := range condGroups {
		groupSQL, groupArgs, err := d.buildCondGroupSQL(ctx, group)
		if err != nil {
			return "", nil, err
		}

		if groupSQL != "" {
			groupConditions = append(groupConditions, "("+groupSQL+")")
			args = append(args, groupArgs...)
		}
	}
	if len(groupConditions) == 0 {
		return "", nil, nil
	}

	// 根据逻辑连接符连接条件组
	var connector string
	if logical == pb.Logical_LogicalOr {
		connector = " OR "
	} else {
		connector = " AND "
	}
	return strings.Join(groupConditions, connector), args, nil
}

// buildCondGroupSQL 构建单个条件组SQL
func (d *DuckDB) buildCondGroupSQL(ctx context.Context, group *pb.SearchCondGroup) (string, []any, error) {
	if len(group.Conds) == 0 {
		return "", nil, nil
	}

	// 处理每个条件
	var conditions []string
	var args []any
	for _, cond := range group.Conds {
		// 获取数据库列名
		columnName := d.formatColumnName(ctx, d.tableID, cond.FieldId)
		if columnName == "" {
			log.DebugContextf(ctx, "Skip condition for field %d: formatColumnName returned empty string (该字段将被忽略)", cond.FieldId)
			continue
		}
		fieldType, _ := d.getFieldType(ctx, cond.FieldId)

		// 处理map字段的子key查询
		if cond.MapKey != "" &&
			(fieldType == pb.EnumFieldType_MAP_KV_FIELD || fieldType == pb.EnumFieldType_MAP_KLIST_FIELD) {
			// JSON路径查询语法，格式为：json_extract(column, '$.key')
			columnName = fmt.Sprintf("json_extract(%s, '$.%s')", columnName, cond.MapKey)
		}

		// 构建条件SQL
		condSQL, condArgs, err := d.buildConditionSQL(columnName, cond.Op, cond.Value)
		if err != nil {
			log.ErrorContextf(ctx, "Failed to build condition for column %s: %v", columnName, err)
			continue
		}
		conditions = append(conditions, condSQL)
		args = append(args, condArgs...)
	}
	if len(conditions) == 0 {
		return "", nil, nil
	}

	// 根据逻辑连接符连接条件
	var connector string
	if group.Logical == pb.Logical_LogicalOr {
		connector = " OR "
	} else {
		connector = " AND "
	}
	return strings.Join(conditions, connector), args, nil
}

// buildConditionSQL 构建单个条件SQL
func (d *DuckDB) buildConditionSQL(columnName string, op pb.Operator, value *pb.SimpleValue) (string, []any, error) {
	var condSQL string
	var args []any

	// 根据操作符和值类型构建条件
	switch op {
	case pb.Operator_eq: // 等于
		condSQL = fmt.Sprintf("%s = ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_ne: // 不等于
		condSQL = fmt.Sprintf("%s != ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_gt: // 大于
		condSQL = fmt.Sprintf("%s > ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_gte: // 大于等于
		condSQL = fmt.Sprintf("%s >= ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_lt: // 小于
		condSQL = fmt.Sprintf("%s < ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_lte: // 小于等于
		condSQL = fmt.Sprintf("%s <= ?", columnName)
		args = append(args, d.getValueFromProtobuf(value))

	case pb.Operator_in: // 包含于列表
		var err error
		condSQL, args, err = d.buildInCondition(columnName, "IN", value, args)
		if err != nil {
			return "", nil, err
		}

	case pb.Operator_notIn: // 不包含于列表
		var err error
		condSQL, args, err = d.buildInCondition(columnName, "NOT IN", value, args)
		if err != nil {
			return "", nil, err
		}

	case pb.Operator_like: // 模糊匹配（字符串型）
		condSQL = fmt.Sprintf("%s LIKE ?", columnName)
		likePattern := "%" + value.GetStr() + "%"
		args = append(args, likePattern)

	case pb.Operator_match: // 匹配（字符串型）
		// 将 match 操作转换为 LIKE 操作，将模式中的每个字符之间插入 %
		if strVal := value.GetStr(); strVal != "" {
			var likePattern string
			for i, ch := range strVal {
				if i > 0 {
					likePattern += "%"
				}
				likePattern += string(ch)
			}
			likePattern = "%" + likePattern + "%"

			condSQL = fmt.Sprintf("%s LIKE ?", columnName)
			args = append(args, likePattern)
		} else {
			return "", nil, fmt.Errorf("invalid value for MATCH operator")
		}

	case pb.Operator_between: // 范围
		// 范围操作，支持整数范围
		if value.GetIntList() != nil && len(value.GetIntList().Values) >= 2 {
			condSQL = fmt.Sprintf("%s BETWEEN ? AND ?", columnName)
			args = append(args, value.GetIntList().Values[0], value.GetIntList().Values[1])
		} else {
			return "", nil, fmt.Errorf("invalid value for BETWEEN operator")
		}

	default:
		return "", nil, fmt.Errorf("unsupported operator: %v", op)
	}
	return condSQL, args, nil
}

// buildInCondition 构建 IN 或 NOT IN 条件
func (d *DuckDB) buildInCondition(columnName string, operator string, value *pb.SimpleValue, args []any) (string, []any, error) {
	return d.buildListCondition(columnName, operator, value, args)
}

// buildListCondition 统一处理列表类型的条件构建
// 处理IN和NOT IN这类需要处理列表值的条件
func (d *DuckDB) buildListCondition(columnName string, operator string, value *pb.SimpleValue, args []any) (string, []any, error) {
	if value.GetStrList() != nil && len(value.GetStrList().Values) > 0 {
		// 需要将[]string转换为[]any
		strValues := value.GetStrList().Values
		values := make([]any, len(strValues))
		for i, v := range strValues {
			values[i] = v
		}
		return d.buildPlaceholderListCondition(columnName, operator, values, args)
	} else if value.GetIntList() != nil && len(value.GetIntList().Values) > 0 {
		// 需要将[]int64转换为[]any
		intValues := value.GetIntList().Values
		values := make([]any, len(intValues))
		for i, v := range intValues {
			values[i] = v
		}
		return d.buildPlaceholderListCondition(columnName, operator, values, args)
	} else {
		return "", nil, fmt.Errorf("invalid value for %s operator", operator)
	}
}

// buildPlaceholderListCondition 构建通用的占位符列表条件
// 这个函数接受任何类型的值列表，构建SQL条件和对应的参数列表
func (d *DuckDB) buildPlaceholderListCondition(columnName string, operator string, values []any, args []any) (string, []any, error) {
	var placeholders []string
	for _, val := range values {
		placeholders = append(placeholders, "?")
		args = append(args, val)
	}
	return fmt.Sprintf("%s %s (%s)", columnName, operator, strings.Join(placeholders, ",")), args, nil
}

// getValueFromProtobuf 从protobuf值中获取实际值
func (d *DuckDB) getValueFromProtobuf(value *pb.SimpleValue) any {
	switch val := value.Value.(type) {
	case *pb.SimpleValue_Str:
		return val.Str
	case *pb.SimpleValue_Int:
		return val.Int
	case *pb.SimpleValue_Float:
		return val.Float
	case *pb.SimpleValue_Time:
		return val.Time
	default:
		return nil
	}
}

// buildOrderByClause 构建ORDER BY子句（优先使用时序排序）
func (d *DuckDB) buildOrderByClause(ctx context.Context, timeSort pb.Sort, sortInfos []*pb.SearchSort) string {
	var orders []string

	// 首先添加时序字段排序（_times字段）
	var timeDirection string
	if timeSort == pb.Sort_Asc {
		timeDirection = "ASC"
	} else {
		timeDirection = "DESC"
	}
	orders = append(orders, fmt.Sprintf("_times %s", timeDirection))

	// 然后添加其他排序字段
	for _, sortInfo := range sortInfos {
		// 获取数据库列名
		columnName := d.formatColumnName(ctx, d.tableID, sortInfo.FieldId)
		if columnName == "" {
			log.DebugContextf(ctx, "Skip sort for field %d: formatColumnName returned empty string (该字段将被忽略)", sortInfo.FieldId)
			continue
		}

		// 跳过_times字段，避免重复排序
		if columnName == "_times" {
			continue
		}

		// 添加排序方向
		var direction string
		if sortInfo.Sort == pb.Sort_Asc {
			direction = "ASC"
		} else {
			direction = "DESC"
		}
		orders = append(orders, fmt.Sprintf("%s %s", columnName, direction))
	}

	if len(orders) == 0 {
		return ""
	}
	return "ORDER BY " + strings.Join(orders, ", ")
}

// buildLimitOffsetClause 构建LIMIT和OFFSET子句
func (d *DuckDB) buildLimitOffsetClause(maxNum uint32, pageInfo *pb.PageInfo) string {
	if pageInfo == nil {
		// 如果没有分页信息，只使用maxNum
		if maxNum > 0 {
			return fmt.Sprintf("LIMIT %d", maxNum)
		}
		return ""
	}

	// 设置默认值
	pageSize := pageInfo.Size
	if pageSize == 0 {
		pageSize = 50 // 默认页大小
	}
	if pageSize > 10000 {
		pageSize = 10000 // 最大页大小
	}
	pageIdx := pageInfo.PageIdx
	if pageIdx == 0 {
		pageIdx = 1 // 默认从第1页开始
	}

	// 计算偏移量
	offset := (pageIdx - 1) * pageSize

	// 如果指定了maxNum且小于pageSize，则使用maxNum
	limit := pageSize
	if maxNum > 0 && maxNum < pageSize {
		limit = maxNum
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

// buildCountQuery 将查询语句转换为计数查询
func buildCountQuery(query string) string {
	// 去掉ORDER BY、LIMIT和OFFSET子句
	lowerQuery := strings.ToLower(query)

	// 查找ORDER BY子句的位置
	orderByIndex := strings.LastIndex(lowerQuery, "order by")
	if orderByIndex == -1 {
		// 查找LIMIT子句的位置
		orderByIndex = strings.LastIndex(lowerQuery, "limit")
	}

	// 如果找到ORDER BY或LIMIT子句，截取前面的部分
	baseQuery := query
	if orderByIndex > 0 {
		baseQuery = query[:orderByIndex]
	}

	// 提取SELECT和FROM之间的部分
	fromIndex := strings.Index(strings.ToLower(baseQuery), "from")
	if fromIndex <= 0 {
		// 如果找不到FROM，返回简单计数
		return "SELECT COUNT(*) FROM (" + query + ") as sub_query"
	}

	// 构造COUNT查询
	countQuery := "SELECT COUNT(*) " + baseQuery[fromIndex:]
	return countQuery
}
