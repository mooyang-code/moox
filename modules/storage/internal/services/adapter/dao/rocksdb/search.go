//go:build !norocksdb && cgo
// +build !norocksdb,cgo

package rocksdb

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SearchFieldInfos 统一搜索接口，支持静态数据和时序数据
func (r *RocksDB) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "RocksDB SearchFieldInfos: %+v", params)

	r.tableID = params.TableID

	switch params.DataType {
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		return r.SearchStaticFieldInfos(ctx, params)

	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		return r.SearchTimingFieldInfos(ctx, params)

	default:
		return nil, 0, fmt.Errorf("invalid data type")
	}
}

// SearchStaticFieldInfos 非时序数据：搜索接口
func (r *RocksDB) SearchStaticFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	tableID := params.TableID
	searchOptions := params.SearchOptions
	pageInfo := params.PageInfo

	// 1. 读取所有行（或按 rowID 过滤）
	getParams := &dao.GetFieldParams{
		TableID:  tableID,
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		RowID:    params.RowID,
		FieldIDs: searchOptions.ReturnFieldIds,
		MaxLimit: 0, // 不限制
	}

	allRows, err := r.GetStaticFieldInfos(ctx, getParams)
	if err != nil {
		return nil, 0, err
	}

	// 2. 应用搜索条件
	var filteredRows []*pb.DocRow
	for _, docRow := range allRows {
		if r.evaluateSearchConditions(ctx, docRow, searchOptions) {
			filteredRows = append(filteredRows, docRow)
		}
	}

	totalCount := uint64(len(filteredRows))

	// 3. 排序
	if len(searchOptions.Sort) > 0 {
		r.sortDocRows(filteredRows, searchOptions.Sort)
	}

	// 4. 分页
	if pageInfo != nil {
		filteredRows = r.paginateResults(filteredRows, pageInfo)
	}

	return filteredRows, totalCount, nil
}

// SearchTimingFieldInfos 时序数据：搜索接口
func (r *RocksDB) SearchTimingFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	tableID := params.TableID
	timeInterval := params.TimeInterval
	searchOptions := params.SearchOptions
	pageInfo := params.PageInfo

	// 时序数据必须指定时间范围
	if timeInterval == nil {
		return nil, 0, fmt.Errorf("时序数据搜索必须指定时间范围")
	}

	// 1. 读取时间范围内的数据
	getParams := &dao.GetFieldParams{
		TableID:      tableID,
		DataType:     pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		RowID:        params.RowID, // 传递 rowID（对于时序数据，通常是 object_id）
		TimeInterval: timeInterval,
		FieldIDs:     searchOptions.ReturnFieldIds,
		MaxLimit:     0,
	}

	allRows, err := r.GetTimingFieldInfos(ctx, getParams)
	if err != nil {
		return nil, 0, err
	}

	// 2. 应用搜索条件
	var filteredRows []*pb.DocRow
	for _, docRow := range allRows {
		if r.evaluateSearchConditions(ctx, docRow, searchOptions) {
			filteredRows = append(filteredRows, docRow)
		}
	}

	totalCount := uint64(len(filteredRows))

	// 3. 排序
	if len(searchOptions.Sort) > 0 {
		r.sortDocRows(filteredRows, searchOptions.Sort)
	} else if params.TimeSort == pb.Sort_Desc {
		// 默认按时间降序
		sort.Slice(filteredRows, func(i, j int) bool {
			return filteredRows[i].Times > filteredRows[j].Times
		})
	} else {
		// 默认按时间升序
		sort.Slice(filteredRows, func(i, j int) bool {
			return filteredRows[i].Times < filteredRows[j].Times
		})
	}

	// 4. 分页
	if pageInfo != nil {
		filteredRows = r.paginateResults(filteredRows, pageInfo)
	}

	return filteredRows, totalCount, nil
}

// ============================================================================
// 辅助函数 - 搜索条件评估
// ============================================================================

// evaluateSearchConditions 评估搜索条件
func (r *RocksDB) evaluateSearchConditions(ctx context.Context, docRow *pb.DocRow, searchOptions *pb.SearchOptions) bool {
	if searchOptions == nil || len(searchOptions.CondGroups) == 0 {
		return true
	}

	// 评估条件组
	groupResults := make([]bool, len(searchOptions.CondGroups))
	for i, condGroup := range searchOptions.CondGroups {
		groupResults[i] = r.evaluateCondGroup(ctx, docRow, condGroup)
	}

	// 应用条件组间的逻辑关系
	if searchOptions.Logical == pb.Logical_LogicalOr {
		for _, result := range groupResults {
			if result {
				return true
			}
		}
		return false
	} else { // AND
		for _, result := range groupResults {
			if !result {
				return false
			}
		}
		return true
	}
}

// evaluateCondGroup 评估单个条件组
func (r *RocksDB) evaluateCondGroup(ctx context.Context, docRow *pb.DocRow, condGroup *pb.SearchCondGroup) bool {
	if len(condGroup.Conds) == 0 {
		return true
	}

	condResults := make([]bool, len(condGroup.Conds))
	for i, cond := range condGroup.Conds {
		condResults[i] = r.evaluateSingleCond(ctx, docRow, cond)
	}

	// 应用条件间的逻辑关系
	if condGroup.Logical == pb.Logical_LogicalOr {
		for _, result := range condResults {
			if result {
				return true
			}
		}
		return false
	} else { // AND
		for _, result := range condResults {
			if !result {
				return false
			}
		}
		return true
	}
}

// evaluateSingleCond 评估单个条件
func (r *RocksDB) evaluateSingleCond(ctx context.Context, docRow *pb.DocRow, cond *pb.SearchCond) bool {
	fieldID := cond.FieldId
	fieldInfo, exists := docRow.Fields[fieldID]
	if !exists {
		return false
	}

	// 根据操作符评估
	switch cond.Op {
	case pb.Operator_eq:
		return r.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_ne:
		return !r.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_gt:
		return r.compareGreater(fieldInfo, cond.Value)
	case pb.Operator_gte:
		return r.compareGreater(fieldInfo, cond.Value) || r.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_lt:
		return r.compareLess(fieldInfo, cond.Value)
	case pb.Operator_lte:
		return r.compareLess(fieldInfo, cond.Value) || r.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_like:
		return r.compareLike(fieldInfo, cond.Value)
	default:
		return false
	}
}

// 比较函数实现（简化版）
func (r *RocksDB) compareEqual(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return fieldInfo.SimpleValue.GetStr() == value.GetStr()
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt() == value.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat() == value.GetFloat()
	default:
		return false
	}
}

func (r *RocksDB) compareGreater(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt() > value.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat() > value.GetFloat()
	default:
		return false
	}
}

func (r *RocksDB) compareLess(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt() < value.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat() < value.GetFloat()
	default:
		return false
	}
}

func (r *RocksDB) compareLike(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	if fieldInfo.FieldType != pb.EnumFieldType_STR_FIELD {
		return false
	}
	fieldStr := fieldInfo.SimpleValue.GetStr()
	pattern := value.GetStr()
	return strings.Contains(fieldStr, strings.Trim(pattern, "*"))
}

// sortDocRows 对结果排序
func (r *RocksDB) sortDocRows(rows []*pb.DocRow, sortRules []*pb.SearchSort) {
	sort.Slice(rows, func(i, j int) bool {
		for _, rule := range sortRules {
			fieldID := rule.FieldId
			fieldI := rows[i].Fields[fieldID]
			fieldJ := rows[j].Fields[fieldID]

			if fieldI == nil || fieldJ == nil {
				continue
			}

			cmp := r.compareFieldValues(fieldI, fieldJ)
			if cmp == 0 {
				continue
			}

			if rule.Sort == pb.Sort_Asc {
				return cmp < 0
			} else {
				return cmp > 0
			}
		}
		return false
	})
}

// compareFieldValues 比较字段值（返回 -1, 0, 1）
func (r *RocksDB) compareFieldValues(a, b *pb.FieldInfo) int {
	switch a.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		valA := a.SimpleValue.GetInt()
		valB := b.SimpleValue.GetInt()
		if valA < valB {
			return -1
		} else if valA > valB {
			return 1
		}
		return 0
	case pb.EnumFieldType_FLOAT_FIELD:
		valA := a.SimpleValue.GetFloat()
		valB := b.SimpleValue.GetFloat()
		if valA < valB {
			return -1
		} else if valA > valB {
			return 1
		}
		return 0
	case pb.EnumFieldType_STR_FIELD:
		valA := a.SimpleValue.GetStr()
		valB := b.SimpleValue.GetStr()
		return strings.Compare(valA, valB)
	default:
		return 0
	}
}

// paginateResults 分页处理
func (r *RocksDB) paginateResults(rows []*pb.DocRow, pageInfo *pb.PageInfo) []*pb.DocRow {
	if pageInfo == nil {
		return rows
	}

	pageIdx := pageInfo.PageIdx
	pageSize := pageInfo.Size
	if pageSize == 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	start := (pageIdx - 1) * pageSize
	end := start + pageSize

	if start >= uint32(len(rows)) {
		return []*pb.DocRow{}
	}
	if end > uint32(len(rows)) {
		end = uint32(len(rows))
	}

	return rows[start:end]
}
