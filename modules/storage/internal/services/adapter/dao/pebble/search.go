package pebble

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (p *Pebble) SearchStaticFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	options := params.SearchOptions
	fieldIDs := []uint32(nil)
	if options != nil {
		fieldIDs = options.ReturnFieldIds
	}
	allRows, err := p.GetStaticFieldInfos(ctx, &dao.GetFieldParams{
		TableID:  params.TableID,
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		RowID:    params.RowID,
		FieldIDs: fieldIDs,
	})
	if err != nil {
		return nil, 0, err
	}
	rows := p.filterRows(ctx, allRows, options)
	totalCount := uint64(len(rows))
	if options != nil && len(options.Sort) > 0 {
		p.sortDocRows(rows, options.Sort)
	}
	return paginateResults(rows, params.PageInfo), totalCount, nil
}

func (p *Pebble) SearchTimingFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	if params.TimeInterval == nil {
		return nil, 0, fmt.Errorf("时序数据搜索必须指定时间范围")
	}
	options := params.SearchOptions
	fieldIDs := []uint32(nil)
	if options != nil {
		fieldIDs = options.ReturnFieldIds
	}
	allRows, err := p.GetTimingFieldInfos(ctx, &dao.GetFieldParams{
		TableID:      params.TableID,
		DataType:     pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		RowID:        params.RowID,
		TimeInterval: params.TimeInterval,
		FieldIDs:     fieldIDs,
	})
	if err != nil {
		return nil, 0, err
	}
	rows := p.filterRows(ctx, allRows, options)
	totalCount := uint64(len(rows))
	if options != nil && len(options.Sort) > 0 {
		p.sortDocRows(rows, options.Sort)
	} else {
		sort.Slice(rows, func(i, j int) bool {
			if params.TimeSort == pb.Sort_Desc {
				return rows[i].Times > rows[j].Times
			}
			return rows[i].Times < rows[j].Times
		})
	}
	return paginateResults(rows, params.PageInfo), totalCount, nil
}

func (p *Pebble) filterRows(ctx context.Context, rows []*pb.DocRow, options *pb.SearchOptions) []*pb.DocRow {
	if options == nil || len(options.CondGroups) == 0 {
		return rows
	}
	filtered := make([]*pb.DocRow, 0, len(rows))
	for _, row := range rows {
		if p.evaluateSearchConditions(ctx, row, options) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (p *Pebble) evaluateSearchConditions(ctx context.Context, docRow *pb.DocRow, searchOptions *pb.SearchOptions) bool {
	if searchOptions == nil || len(searchOptions.CondGroups) == 0 {
		return true
	}
	groupResults := make([]bool, len(searchOptions.CondGroups))
	for i, condGroup := range searchOptions.CondGroups {
		groupResults[i] = p.evaluateCondGroup(ctx, docRow, condGroup)
	}
	if searchOptions.Logical == pb.Logical_LogicalOr {
		for _, result := range groupResults {
			if result {
				return true
			}
		}
		return false
	}
	for _, result := range groupResults {
		if !result {
			return false
		}
	}
	return true
}

func (p *Pebble) evaluateCondGroup(ctx context.Context, docRow *pb.DocRow, condGroup *pb.SearchCondGroup) bool {
	if condGroup == nil || len(condGroup.Conds) == 0 {
		return true
	}
	condResults := make([]bool, len(condGroup.Conds))
	for i, cond := range condGroup.Conds {
		condResults[i] = p.evaluateSingleCond(ctx, docRow, cond)
	}
	if condGroup.Logical == pb.Logical_LogicalOr {
		for _, result := range condResults {
			if result {
				return true
			}
		}
		return false
	}
	for _, result := range condResults {
		if !result {
			return false
		}
	}
	return true
}

func (p *Pebble) evaluateSingleCond(_ context.Context, docRow *pb.DocRow, cond *pb.SearchCond) bool {
	if docRow == nil || cond == nil {
		return false
	}
	fieldInfo := docRow.Fields[cond.FieldId]
	if fieldInfo == nil {
		return false
	}
	switch cond.Op {
	case pb.Operator_eq:
		return p.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_ne:
		return !p.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_gt:
		return p.compareGreater(fieldInfo, cond.Value)
	case pb.Operator_gte:
		return p.compareGreater(fieldInfo, cond.Value) || p.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_lt:
		return p.compareLess(fieldInfo, cond.Value)
	case pb.Operator_lte:
		return p.compareLess(fieldInfo, cond.Value) || p.compareEqual(fieldInfo, cond.Value)
	case pb.Operator_like:
		return p.compareLike(fieldInfo, cond.Value)
	case pb.Operator_in:
		return p.compareIn(fieldInfo, cond.Value)
	case pb.Operator_notIn:
		return !p.compareIn(fieldInfo, cond.Value)
	default:
		return false
	}
}

func (p *Pebble) compareEqual(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	if value == nil {
		return false
	}
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

func (p *Pebble) compareGreater(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	if value == nil {
		return false
	}
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt() > value.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat() > value.GetFloat()
	default:
		return false
	}
}

func (p *Pebble) compareLess(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	if value == nil {
		return false
	}
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt() < value.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat() < value.GetFloat()
	default:
		return false
	}
}

func (p *Pebble) compareLike(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	return fieldInfo.FieldType == pb.EnumFieldType_STR_FIELD &&
		value != nil &&
		strings.Contains(fieldInfo.SimpleValue.GetStr(), value.GetStr())
}

func (p *Pebble) compareIn(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
	if value == nil {
		return false
	}
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		for _, candidate := range value.GetStrList().Values {
			if fieldInfo.SimpleValue.GetStr() == candidate {
				return true
			}
		}
	case pb.EnumFieldType_INT_FIELD:
		for _, candidate := range value.GetIntList().Values {
			if fieldInfo.SimpleValue.GetInt() == candidate {
				return true
			}
		}
	}
	return false
}

func (p *Pebble) sortDocRows(rows []*pb.DocRow, sortRules []*pb.SearchSort) {
	sort.Slice(rows, func(i, j int) bool {
		for _, rule := range sortRules {
			fieldI := rows[i].Fields[rule.FieldId]
			fieldJ := rows[j].Fields[rule.FieldId]
			if fieldI == nil || fieldJ == nil {
				continue
			}
			cmp := compareFieldValues(fieldI, fieldJ)
			if cmp == 0 {
				continue
			}
			if rule.Sort == pb.Sort_Asc {
				return cmp < 0
			}
			return cmp > 0
		}
		return false
	})
}

func compareFieldValues(a, b *pb.FieldInfo) int {
	switch a.FieldType {
	case pb.EnumFieldType_INT_FIELD:
		return compareInt(a.SimpleValue.GetInt(), b.SimpleValue.GetInt())
	case pb.EnumFieldType_FLOAT_FIELD:
		return compareFloat(a.SimpleValue.GetFloat(), b.SimpleValue.GetFloat())
	case pb.EnumFieldType_STR_FIELD:
		return strings.Compare(a.SimpleValue.GetStr(), b.SimpleValue.GetStr())
	default:
		return 0
	}
}

func compareInt(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareFloat(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func paginateResults(rows []*pb.DocRow, pageInfo *pb.PageInfo) []*pb.DocRow {
	if pageInfo == nil {
		return rows
	}
	pageIdx := pageInfo.PageIdx
	if pageIdx == 0 {
		pageIdx = 1
	}
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
