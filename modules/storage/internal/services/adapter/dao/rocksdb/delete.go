//go:build !norocksdb && cgo
// +build !norocksdb,cgo

package rocksdb

import (
	"context"
	"fmt"
	"time"

	"github.com/linxGnu/grocksdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// DeleteRows 统一删除数据接口(软删除，仅静态数据支持)
func (r *RocksDB) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
	rsp := &pb.DeleteRowsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		DeletedCount: 0,
	}

	r.tableID = params.TableID

	// 时序数据不支持删除
	if params.DataType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
		return nil, fmt.Errorf("时序数据不支持删除操作")
	}

	// 仅静态数据支持删除
	if params.DataType != pb.EnumDataTypeCategory_STATIC_DATA_TYPE {
		return nil, fmt.Errorf("invalid data type")
	}

	if len(params.RowIDs) == 0 {
		return rsp, nil
	}

	// 批量设置删除标记
	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	deletedCount := uint64(0)
	currentTime := time.Now().Format("2006-01-02 15:04:05")

	for _, rowID := range params.RowIDs {
		// 检查是否已删除
		deleted, err := r.isRowDeleted(params.TableID, rowID)
		if err != nil {
			log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
			continue
		}
		if deleted {
			log.DebugContextf(ctx, "行[%s]已删除，跳过", rowID)
			continue
		}

		// 设置删除标记
		deleteKey := buildDeletedKey(params.TableID, rowID)
		batch.Put([]byte(deleteKey), []byte("1"))

		// 设置删除时间
		deleteTimeKey := buildDeletedTimeKey(params.TableID, rowID)
		batch.Put([]byte(deleteTimeKey), []byte(currentTime))

		deletedCount++
	}

	// 提交批量操作
	err := r.db.Write(r.wo, batch)
	if err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("删除操作失败: %v", err))
	}

	rsp.DeletedCount = deletedCount
	log.InfoContextf(ctx, "静态数据删除成功，共 %d 行", deletedCount)
	return rsp, nil
}
