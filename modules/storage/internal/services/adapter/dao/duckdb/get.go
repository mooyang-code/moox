// Package duckdb duckdb相关逻辑
package duckdb

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// GetFieldInfos 统一获取数据接口，DuckDB支持静态数据和时序数据存储
func (d *DuckDB) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	d.tableID = params.TableID
	tableID := params.TableID
	maxLimit := params.MaxLimit
	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return d.GetTimingFieldInfos(ctx, tableID, params.TimeInterval, params.FieldIDs, params.MapKeys, maxLimit)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return d.GetStaticFieldInfos(ctx, tableID, params.RowID, params.FieldIDs, params.MapKeys, maxLimit)
	default:
		// 处理默认情况，这里应该添加一个返回
		return nil, fmt.Errorf("invalid data type")
	}
}

// GetStaticFieldInfos 静态数据：统一获取value接口
func (d *DuckDB) GetStaticFieldInfos(ctx context.Context, tableID string, rowID string,
	fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList, maxLimit uint32) ([]*pb.DocRow, error) {
	d.data = make(map[string]any)
	d.isGetAll = len(fieldIDs) == 0 || (len(fieldIDs) == 1 && fieldIDs[0] == 0)

	// 构建SQL查询
	var query string
	var args []any
	colNameList := d.buildColumnNameList(fieldIDs)
	if rowID == "" {
		// 查询所有未删除的行
		query = fmt.Sprintf("SELECT %s FROM %s WHERE %s", colNameList, tableID, d.buildNotDeletedCondition())
	} else {
		// 查询指定的未删除行
		query = fmt.Sprintf("SELECT %s FROM %s WHERE _row_id = ? AND %s", colNameList, tableID, d.buildNotDeletedCondition())
		args = append(args, rowID)
	}

	// 添加LIMIT子句限制返回结果数量
	if maxLimit > 0 {
		query += fmt.Sprintf(" LIMIT %d", maxLimit)
	}

	// 通过通用函数执行查询并处理结果
	return d.executeQuery(ctx, query, args, mapKeys, func(docRow *pb.DocRow, columns []string, values []any) {
		// 从结果中获取row_id
		for i, col := range columns {
			if col == "_row_id" {
				if rowIDVal, ok := values[i].(string); ok {
					docRow.RowId = rowIDVal
				}
			}
		}
	})
}

// GetTimingFieldInfos 时序数据：统一获取value接口
func (d *DuckDB) GetTimingFieldInfos(ctx context.Context, tableID string, timeInterval *pb.TimeInterval,
	fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList, maxLimit uint32) ([]*pb.DocRow, error) {
	d.data = make(map[string]any)
	d.isGetAll = len(fieldIDs) == 0 || (len(fieldIDs) == 1 && fieldIDs[0] == 0)

	// 构建SQL查询
	var query string
	var args []any
	colNameList := d.buildColumnNameList(fieldIDs)
	timeCondition, timeArgs := d.buildTimeIntervalCond(timeInterval)
	// 组合时间条件和未删除条件
	whereCondition := fmt.Sprintf("(%s) AND %s", timeCondition, d.buildNotDeletedCondition())
	query = fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY _times", colNameList, tableID, whereCondition)
	args = append(args, timeArgs...)

	// 添加LIMIT子句限制返回结果数量
	if maxLimit > 0 {
		query += fmt.Sprintf(" LIMIT %d", maxLimit)
	}

	// 通过通用函数执行查询并处理结果
	return d.executeQuery(ctx, query, args, mapKeys, func(docRow *pb.DocRow, columns []string, values []any) {
		// 从结果中获取row_id和times
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
}

// executeQuery 执行查询并处理结果的通用函数
func (d *DuckDB) executeQuery(ctx context.Context, query string, args []any,
	mapKeys map[uint32]*pb.KeyList,
	processRowFunc func(docRow *pb.DocRow, columns []string, values []any)) ([]*pb.DocRow, error) {
	// 使用通用的executeQueryWithRowProcessor函数，传入自定义的行处理器
	return d.executeQueryWithRowProcessor(ctx, query, args, mapKeys, processRowFunc)
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// buildTimeIntervalCond 构建时间范围条件
func (d *DuckDB) buildTimeIntervalCond(timeInterval *pb.TimeInterval) (string, []any) {
	var condition string
	var args []any

	// 如果时间范围为空，返回恒真条件
	if timeInterval == nil {
		return "1=1", args
	}

	// 开始时间
	if timeInterval.Start != "" {
		condition += "_times >= ?"
		args = append(args, timeInterval.Start)
	}

	// 结束时间
	if timeInterval.GetEnd() != "" {
		if condition != "" {
			condition += " AND "
		}
		condition += "_times <= ?"
		args = append(args, timeInterval.GetEnd())
	}

	// 如果没有条件，返回恒真条件
	if condition == "" {
		condition = "1=1"
	}
	return condition, args
}
