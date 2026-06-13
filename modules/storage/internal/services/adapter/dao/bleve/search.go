package bleve

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SearchFieldInfos 统一搜索接口，支持静态数据和时序数据
func (b *Bleve) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "********** Bleve SearchFieldInfos:%+v", params)
	// 获取或创建索引
	indexPath := b.getTableIndexPath(params.TableID)
	index, err := getIndex(ctx, indexPath)
	if err != nil {
		return nil, 0, err
	}
	// 操作完成后关闭索引连接
	defer index.Close()

	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return b.SearchTimingFieldInfos(ctx, params.TableID, params.TimeInterval, params.TimeSort, params.SearchOptions, params.PageInfo, index)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return b.SearchStaticFieldInfos(ctx, params.TableID, params.RowID, params.SearchOptions, params.PageInfo, index)
	default:
		// 处理默认情况
		return nil, 0, fmt.Errorf("Invalid data type")
	}
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

// SearchStaticFieldInfos 非时序数据：搜索接口
func (b *Bleve) SearchStaticFieldInfos(ctx context.Context, tableID string, rowID string, searchOp *pb.SearchOptions, pageInfo *pb.PageInfo, index bleve.Index) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "********** Bleve SearchStaticFieldInfos:%s", tableID)
	// 构建查询
	var mainQuery query.Query

	// 如果指定了行ID，则精确查询该行
	if rowID != "" {
		baseQuery := bleve.NewDocIDQuery([]string{rowID})
		mainQuery = b.buildQuery(baseQuery)
	} else {
		// 构建搜索条件
		baseQuery, err := b.buildSearchQuery(ctx, searchOp)
		if err != nil {
			return nil, 0, err
		}
		mainQuery = b.buildQuery(baseQuery)
	}

	// 确定默认排序字段
	var defaultSortFields []string

	// 构建搜索请求并执行搜索
	return b.executeSearchQuery(ctx, mainQuery, searchOp, pageInfo, defaultSortFields, index)
}

// SearchTimingFieldInfos 时序数据：搜索接口（支持时序排序）
func (b *Bleve) SearchTimingFieldInfos(ctx context.Context, tableID string, timeInterval *pb.TimeInterval,
	timeSort pb.Sort, searchOp *pb.SearchOptions, pageInfo *pb.PageInfo, index bleve.Index) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "********** Bleve 时序搜索开始: tableID=%s", tableID)
	// 构建基本查询
	baseQuery, err := b.buildSearchQuery(ctx, searchOp)
	if err != nil {
		log.ErrorContextf(ctx, "构建搜索条件失败: %v", err)
		baseQuery = bleve.NewMatchAllQuery()
	}

	// 构建时间范围查询
	if timeInterval != nil && (timeInterval.Start != "" || timeInterval.GetEnd() != "") {
		var minTime, maxTime *string
		if timeInterval.Start != "" {
			minTime = &timeInterval.Start
		}
		if timeInterval.GetEnd() != "" {
			end := timeInterval.GetEnd()
			maxTime = &end
		}

		// 使用utils工具函数转换时间
		minTimePtr, _ := utils.StringPtrToTimePtr(minTime)
		maxTimePtr, _ := utils.StringPtrToTimePtr(maxTime)

		// 确保时间指针不为nil，避免传入nil给NewDateRangeQuery
		var minTimeObj, maxTimeObj time.Time
		if minTimePtr != nil {
			minTimeObj = *minTimePtr
		}
		if maxTimePtr != nil {
			maxTimeObj = *maxTimePtr
		}

		// 记录时间范围调试信息
		log.DebugContextf(ctx, "时间范围过滤: start=%v, end=%v", minTimeObj, maxTimeObj)

		timeQuery := bleve.NewDateRangeQuery(minTimeObj, maxTimeObj)
		setQueryField(timeQuery, "_times")
		log.DebugContextf(ctx, "时间范围查询(JSON): %s", debugQuery(timeQuery))

		// 组合查询：基本查询 AND 时间范围
		conjunctionQuery := bleve.NewConjunctionQuery(baseQuery, timeQuery)
		baseQuery = conjunctionQuery
	}

	// 添加未删除条件
	finalQuery := b.buildQuery(baseQuery)

	// 根据时序排序参数构建默认排序字段
	var defaultSortFields []string
	if timeSort == pb.Sort_Asc {
		defaultSortFields = []string{"_times"}
	} else {
		defaultSortFields = []string{"-_times"} // 降序
	}

	// 执行
	rows, total, execErr := b.executeSearchQuery(ctx, finalQuery, searchOp, pageInfo, defaultSortFields, index)
	if execErr != nil {
		return nil, 0, execErr
	}
	return rows, total, nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

func debugQuery(query query.Query) string {
	queryJSON, err := json.MarshalIndent(query, "", "  ")
	if err != nil {
		fmt.Println("Error marshaling query:", err)
		return ""
	}
	return string(queryJSON)
}

// executeSearchQuery 执行搜索查询，处理分页、排序和结果转换
func (b *Bleve) executeSearchQuery(ctx context.Context, mainQuery query.Query,
	searchOp *pb.SearchOptions, pageInfo *pb.PageInfo, defaultSortFields []string, index bleve.Index) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "执行搜索: 查询结构(JSON) %s", debugQuery(mainQuery))

	// 创建搜索请求
	searchRequest := bleve.NewSearchRequest(mainQuery)

	// 设置分页
	if pageInfo != nil {
		if pageInfo.Size > 0 {
			searchRequest.Size = int(pageInfo.Size)
		} else {
			searchRequest.Size = 10 // 默认每页10条
		}
		if pageInfo.PageIdx > 0 {
			searchRequest.From = int(pageInfo.PageIdx-1) * searchRequest.Size
		}
	} else if searchOp != nil && searchOp.MaxNum > 0 {
		searchRequest.Size = int(searchOp.MaxNum)
	} else {
		searchRequest.Size = 100 // 默认最多返回100条
	}

	// 设置排序
	if searchOp != nil && len(searchOp.Sort) > 0 {
		sortFields := b.buildSortFields(searchOp.Sort)
		searchRequest.SortBy(sortFields)
	} else if len(defaultSortFields) > 0 {
		// 使用默认排序字段
		searchRequest.SortBy(defaultSortFields)
	}

	// 记录搜索请求配置，包含分页和排序信息
	log.DebugContextf(ctx, "搜索请求配置: size=%d, from=%d", searchRequest.Size, searchRequest.From)

	// 请求所有字段
	searchRequest.Fields = []string{"*"}

	// 执行搜索
	searchResults, err := index.Search(searchRequest)
	if err != nil {
		log.ErrorContextf(ctx, "Bleve 搜索执行错误: %v", err)
		return nil, 0, err
	}

	// 记录搜索结果统计信息
	log.DebugContextf(ctx, "搜索结果: total=%d, max_score=%.2f, took=%v",
		searchResults.Total, searchResults.MaxScore, searchResults.Took)

	// 处理搜索结果
	var results []*pb.DocRow
	for _, hit := range searchResults.Hits {
		docRow := b.documentToDocRow(hit.Fields)
		results = append(results, docRow)
	}
	return results, searchResults.Total, nil
}

// buildSearchQuery 构建搜索查询
func (b *Bleve) buildSearchQuery(ctx context.Context, searchOp *pb.SearchOptions) (query.Query, error) {
	if searchOp == nil || len(searchOp.CondGroups) == 0 {
		log.DebugContextf(ctx, "未提供搜索条件，使用全量匹配查询(MatchAll)")
		return bleve.NewMatchAllQuery(), nil
	}

	// 处理条件组
	var condGroupQueries []query.Query
	for i, condGroup := range searchOp.CondGroups {
		if len(condGroup.Conds) == 0 {
			log.DebugContextf(ctx, "Condition group %d is empty, skipping", i)
			continue
		}
		log.DebugContextf(ctx, "处理条件组 %d: 条件数=%d, 组内逻辑=%v",
			i, len(condGroup.Conds), condGroup.Logical)

		groupQuery := b.processConditionGroup(ctx, condGroup)
		if groupQuery != nil {
			condGroupQueries = append(condGroupQueries, groupQuery)
		}
	}

	// 根据组间逻辑连接符组合条件组
	return b.combineFinalQuery(ctx, condGroupQueries, searchOp.Logical), nil
}

// processConditionGroup 处理单个条件组
func (b *Bleve) processConditionGroup(ctx context.Context, condGroup *pb.SearchCondGroup) query.Query {
	var condQueries []query.Query
	for j, cond := range condGroup.Conds {
		colName := b.fieldID2ColName(uint64(cond.FieldId), cond.MapKey)
		fieldType, _ := b.getFieldType(ctx, cond.FieldId)
		log.DebugContextf(ctx, "  处理条件 %d: 字段(列)=%s, 类型=%v, 操作符=%v, 值=%v, map_key=%s", j, colName, fieldType, cond.Op, cond.Value, cond.MapKey)

		condQuery := b.buildConditionQuery(ctx, cond, colName)
		if condQuery != nil {
			condQueries = append(condQueries, condQuery)
		}
	}

	// 根据组内逻辑连接符组合条件
	if len(condQueries) == 0 {
		return nil
	}
	if condGroup.Logical == pb.Logical_LogicalOr {
		return bleve.NewDisjunctionQuery(condQueries...)
	}
	return bleve.NewConjunctionQuery(condQueries...)
}

// buildConditionQuery 根据操作符构建单个条件查询
func (b *Bleve) buildConditionQuery(ctx context.Context, cond *pb.SearchCond, colName string) query.Query {
	if cond.Value == nil {
		return nil
	}

	switch cond.Op {
	case pb.Operator_eq:
		return b.buildTermQuery(colName, cond.Value)
	case pb.Operator_ne:
		termQuery := b.buildTermQuery(colName, cond.Value)
		return b.buildNotQuery(termQuery)
	case pb.Operator_gt:
		return b.buildRangeQuery(colName, cond.Value, true, false)
	case pb.Operator_gte:
		return b.buildRangeQuery(colName, cond.Value, true, true)
	case pb.Operator_lt:
		return b.buildRangeQuery(colName, cond.Value, false, false)
	case pb.Operator_lte:
		return b.buildRangeQuery(colName, cond.Value, false, true)
	case pb.Operator_in:
		return b.buildInQuery(colName, cond.Value)
	case pb.Operator_notIn:
		inQuery := b.buildInQuery(colName, cond.Value)
		return b.buildNotQuery(inQuery)
	case pb.Operator_like:
		if cond.Value.GetStr() == "" {
			return nil
		}
		prefixQuery := bleve.NewPrefixQuery(cond.Value.GetStr())
		setQueryField(prefixQuery, colName)
		return prefixQuery
	case pb.Operator_match:
		if cond.Value.GetStr() == "" {
			return nil
		}
		matchQuery := bleve.NewMatchQuery(cond.Value.GetStr())
		setQueryField(matchQuery, colName)
		return matchQuery
	default:
		log.WarnContextf(ctx, "不支持的操作符: %v", cond.Op)
		return nil
	}
}

// buildNotQuery 构建NOT查询
func (b *Bleve) buildNotQuery(innerQuery query.Query) query.Query {
	if innerQuery == nil {
		return nil
	}
	boolQuery := bleve.NewBooleanQuery()
	boolQuery.AddMustNot(innerQuery)
	return boolQuery
}

// combineFinalQuery 组合最终查询
func (b *Bleve) combineFinalQuery(ctx context.Context, condGroupQueries []query.Query, logical pb.Logical) query.Query {
	if len(condGroupQueries) == 0 {
		log.DebugContextf(ctx, "未发现有效条件，使用全量匹配查询(MatchAll)")
		return bleve.NewMatchAllQuery()
	}

	var finalQuery query.Query
	if logical == pb.Logical_LogicalOr {
		finalQuery = bleve.NewDisjunctionQuery(condGroupQueries...)
		log.DebugContextf(ctx, "最终查询: OR(析取) 组合, 子查询数量=%d", len(condGroupQueries))
	} else {
		finalQuery = bleve.NewConjunctionQuery(condGroupQueries...)
		log.DebugContextf(ctx, "最终查询: AND(合取) 组合, 子查询数量=%d", len(condGroupQueries))
	}
	return finalQuery
}

// setQueryField 使用反射为查询对象设置字段
func setQueryField(q query.Query, colName string) {
	// 使用反射调用SetField方法
	if q != nil {
		v := reflect.ValueOf(q)
		if v.Kind() == reflect.Ptr && !v.IsNil() {
			m := v.MethodByName("SetField")
			if m.IsValid() {
				m.Call([]reflect.Value{reflect.ValueOf(colName)})
			}
		}
	}
}

// buildTermQuery 构建Term查询
func (b *Bleve) buildTermQuery(colName string, value *pb.SimpleValue) query.Query {
	var q query.Query

	switch {
	case value.GetStr() != "":
		q = bleve.NewTermQuery(value.GetStr())
		setQueryField(q, colName)
	case value.GetInt() != 0:
		q = bleve.NewTermQuery(strconv.FormatInt(value.GetInt(), 10))
		setQueryField(q, colName)
	case value.GetFloat() != 0:
		q = bleve.NewTermQuery(strconv.FormatFloat(value.GetFloat(), 'f', -1, 64))
		setQueryField(q, colName)
	default:
		return nil
	}
	return q
}

// buildRangeQuery 构建范围查询
func (b *Bleve) buildRangeQuery(colName string, value *pb.SimpleValue, isMin, inclusive bool) query.Query {
	switch {
	case value.GetInt() != 0:
		return b.buildNumericRangeQuery(colName, float64(value.GetInt()), isMin, inclusive)

	case value.GetFloat() != 0:
		return b.buildNumericRangeQuery(colName, value.GetFloat(), isMin, inclusive)

	case value.GetTime() != "":
		return b.buildTimeRangeQuery(colName, value.GetTime(), isMin, inclusive)

	case value.GetStr() != "":
		return b.buildStringRangeQuery(colName, value.GetStr(), isMin, inclusive)
	}
	return nil
}

// buildNumericRangeQuery 构建数值范围查询（整数和浮点数通用）
func (b *Bleve) buildNumericRangeQuery(colName string, val float64, isMin, inclusive bool) query.Query {
	var min, max *float64

	if isMin {
		if !inclusive {
			// 不包含等于，需要稍微调整
			val = val + 0.000001
		}
		min = &val
		numQuery := bleve.NewNumericRangeQuery(min, nil)
		setQueryField(numQuery, colName)
		log.Infof("数值范围查询: 字段=%s, 下界=%f, 含等于=%v", colName, *min, inclusive)
		return numQuery
	} else {
		if !inclusive {
			// 不包含等于，需要稍微调整
			val = val - 0.000001
		}
		max = &val
		numQuery := bleve.NewNumericRangeQuery(nil, max)
		setQueryField(numQuery, colName)
		log.Infof("数值范围查询: 字段=%s, 上界=%f, 含等于=%v", colName, *max, inclusive)
		return numQuery
	}
}

// buildTimeRangeQuery 构建时间范围查询
func (b *Bleve) buildTimeRangeQuery(colName string, timeStr string, isMin, inclusive bool) query.Query {
	timeObj, err := utils.StringToTime(timeStr)
	if err != nil {
		// 时间解析失败，记录日志并返回nil
		log.Warnf("解析时间字符串失败: %s, 错误: %v", timeStr, err)
		return nil
	}

	var minTime, maxTime time.Time

	if isMin {
		if !inclusive {
			// 不包含等于，加1毫秒
			timeObj = timeObj.Add(time.Millisecond)
		}
		minTime = timeObj
		// maxTime 使用零值表示无上限
		dateQuery := bleve.NewDateRangeQuery(minTime, time.Time{})
		setQueryField(dateQuery, colName)
		log.Infof("时间范围查询: 字段=%s, 起始=%v, 含等于=%v", colName, minTime, inclusive)
		return dateQuery
	} else {
		if !inclusive {
			// 不包含等于，减1毫秒
			timeObj = timeObj.Add(-time.Millisecond)
		}
		maxTime = timeObj
		// minTime 使用零值表示无下限
		dateQuery := bleve.NewDateRangeQuery(time.Time{}, maxTime)
		setQueryField(dateQuery, colName)
		log.Infof("时间范围查询: 字段=%s, 截止=%v, 含等于=%v", colName, maxTime, inclusive)
		return dateQuery
	}
}

// buildStringRangeQuery 构建字符串范围查询
func (b *Bleve) buildStringRangeQuery(colName string, str string, isMin, inclusive bool) query.Query {
	var minStr, maxStr string

	if isMin {
		minStr = str
		if !inclusive {
			// 字符串不包含的情况比较复杂，这里先保持原有逻辑
			// 实际可能需要根据具体业务需求调整
			minStr = str + "\x01" // 添加最小可打印字符
		}
		trq := bleve.NewTermRangeQuery(minStr, "")
		setQueryField(trq, colName)
		return trq
	} else {
		maxStr = str
		if !inclusive {
			// 字符串不包含的情况，减少最后一个字符的值
			if len(str) > 0 {
				maxStr = str[:len(str)-1] + string(rune(str[len(str)-1])-1)
			}
		}
		trq := bleve.NewTermRangeQuery("", maxStr)
		setQueryField(trq, colName)
		return trq
	}
}

// buildInQuery 构建IN查询
func (b *Bleve) buildInQuery(colName string, value *pb.SimpleValue) query.Query {
	if value == nil {
		return nil
	}

	// 创建析取查询（OR）
	disjunctionQuery := bleve.NewDisjunctionQuery()

	// 根据值类型获取列表值
	if strList := value.GetStrList(); strList != nil && len(strList.Values) > 0 {
		for _, strVal := range strList.Values {
			termQuery := bleve.NewTermQuery(strVal)
			setQueryField(termQuery, colName)
			disjunctionQuery.AddQuery(termQuery)
		}
	} else if intList := value.GetIntList(); intList != nil && len(intList.Values) > 0 {
		for _, intVal := range intList.Values {
			termQuery := bleve.NewTermQuery(strconv.FormatInt(intVal, 10))
			setQueryField(termQuery, colName)
			disjunctionQuery.AddQuery(termQuery)
		}
	}
	return disjunctionQuery
}

// buildSortFields 构建排序字段
func (b *Bleve) buildSortFields(sortInfos []*pb.SearchSort) []string {
	if len(sortInfos) == 0 {
		return nil
	}

	var sortFields []string
	for _, sort := range sortInfos {
		colName := b.fieldID2ColName(uint64(sort.FieldId), sort.MapKey)

		// 添加排序方向
		if sort.Sort == pb.Sort_Desc {
			colName = "-" + colName // 降序
		}
		sortFields = append(sortFields, colName)
	}
	return sortFields
}
