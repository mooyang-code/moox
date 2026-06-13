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

// DeleteRows 统一删除数据接口(软删除，设置_deleted字段)
func (d *DuckDB) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
	// 初始化响应
	rsp := &pb.DeleteRowsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		DeletedCount: 0,
	}
	d.tableID = params.TableID

	// 根据数据类型处理删除逻辑
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 处理时序数据删除
		deletedCount, err := d.deleteTimingData(ctx, params.TableID, params.TimeInterval, params.RowIDs)
		if err != nil {
			log.ErrorContextf(ctx, "删除时序数据失败: %v", err)
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = fmt.Sprintf("删除时序数据失败: %v", err)
			return rsp, nil
		}
		rsp.DeletedCount = deletedCount
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 处理静态数据删除
		deletedCount, err := d.deleteStaticData(ctx, params.TableID, params.RowIDs)
		if err != nil {
			log.ErrorContextf(ctx, "删除静态数据失败: %v", err)
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = fmt.Sprintf("删除静态数据失败: %v", err)
			return rsp, nil
		}
		rsp.DeletedCount = deletedCount
	default:
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = "不支持的数据类型"
		return rsp, nil
	}
	return rsp, nil
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

// deleteTimingData 删除时序数据（软删除）
func (d *DuckDB) deleteTimingData(ctx context.Context, tableID string, timeInterval *pb.TimeInterval, rowIDs []string) (uint64, error) {
	var whereConditions []string
	var args []interface{}
	argIndex := 1

	// 构建WHERE条件
	if timeInterval != nil {
		// 按时间区间删除
		if timeInterval.GetStart() != "" {
			whereConditions = append(whereConditions, fmt.Sprintf("_times >= $%d", argIndex))
			args = append(args, timeInterval.GetStart())
			argIndex++
		}
		if timeInterval.GetEnd() != "" {
			whereConditions = append(whereConditions, fmt.Sprintf("_times <= $%d", argIndex))
			args = append(args, timeInterval.GetEnd())
			argIndex++
		}
	}

	if len(rowIDs) > 0 {
		// 按行ID删除
		placeholders := make([]string, len(rowIDs))
		for i, rowID := range rowIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIndex)
			args = append(args, rowID)
			argIndex++
		}
		whereConditions = append(whereConditions,
			fmt.Sprintf("_row_id IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(whereConditions) == 0 {
		return 0, fmt.Errorf("没有指定删除条件")
	}

	// 添加未删除条件（避免重复删除）
	whereConditions = append(whereConditions, "_deleted = 0")

	// 构建软删除SQL（设置c_deleted=1和删除时间）
	updateSQL := fmt.Sprintf(`
		UPDATE %s 
		SET _deleted = 1, _deleted_time = $%d 
		WHERE %s`,
		tableID,
		argIndex,
		strings.Join(whereConditions, " AND "))

	// 添加删除时间参数
	args = append(args, time.Now().Format("2006-01-02 15:04:05"))
	log.InfoContextf(ctx, "执行时序数据软删除SQL: %s, args: %v", updateSQL, args)

	// 执行更新
	result, err := d.db.ExecContext(ctx, updateSQL, args...)
	if err != nil {
		return 0, fmt.Errorf("执行软删除失败: %v", err)
	}

	// 获取影响的行数
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.WarnContextf(ctx, "获取影响行数失败: %v", err)
		return 0, nil
	}
	return uint64(rowsAffected), nil
}

// deleteStaticData 删除静态数据（软删除）
func (d *DuckDB) deleteStaticData(ctx context.Context, tableID string, rowIDs []string) (uint64, error) {
	if len(rowIDs) == 0 {
		return 0, fmt.Errorf("静态数据删除必须指定行ID")
	}

	var args []interface{}
	placeholders := make([]string, len(rowIDs))
	for i, rowID := range rowIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args = append(args, rowID)
	}

	// 构建软删除SQL（设置c_deleted=1和删除时间）
	updateSQL := fmt.Sprintf(`
		UPDATE %s 
		SET _deleted = 1, _deleted_time = $%d 
		WHERE _row_id IN (%s) AND _deleted = 0`,
		tableID,
		len(args)+1,
		strings.Join(placeholders, ","))

	// 添加删除时间参数
	args = append(args, time.Now().Format("2006-01-02 15:04:05"))

	log.InfoContextf(ctx, "执行静态数据软删除SQL: %s, args: %v", updateSQL, args)

	// 执行更新
	result, err := d.db.ExecContext(ctx, updateSQL, args...)
	if err != nil {
		return 0, fmt.Errorf("执行软删除失败: %v", err)
	}

	// 获取影响的行数
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.WarnContextf(ctx, "获取影响行数失败: %v", err)
		return 0, nil
	}
	return uint64(rowsAffected), nil
}
