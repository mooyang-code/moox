package bleve

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// DeleteRows 统一删除数据接口(软删除，设置_deleted字段)
func (b *Bleve) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
	rsp := &pb.DeleteRowsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		DeletedCount: 0,
	}

	// 获取或创建索引
	indexPath := b.getTableIndexPath(params.TableID)
	index, err := getIndex(ctx, indexPath)
	if err != nil {
		log.ErrorContextf(ctx, "获取索引失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = fmt.Sprintf("获取索引失败: %v", err)
		return rsp, nil
	}
	// 操作完成后关闭索引连接
	defer index.Close()

	// 检查索引是否有效
	if index == nil {
		log.ErrorContextf(ctx, "索引为空，无法执行删除操作")
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = "索引为空，无法执行删除操作"
		return rsp, nil
	}

	// 根据数据类型处理删除逻辑
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 处理时序数据删除
		deletedCount, err := b.deleteTimingData(ctx, index, params.TimeInterval, params.RowIDs)
		if err != nil {
			log.ErrorContextf(ctx, "删除时序数据失败: %v", err)
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = fmt.Sprintf("删除时序数据失败: %v", err)
			return rsp, nil
		}
		rsp.DeletedCount = deletedCount
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 处理静态数据删除
		deletedCount, err := b.deleteStaticData(ctx, index, params.RowIDs)
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
func (b *Bleve) deleteTimingData(ctx context.Context, index bleve.Index,
	timeInterval *pb.TimeInterval, rowIDs []string) (uint64, error) {
	if index == nil {
		return 0, fmt.Errorf("索引为空，无法执行删除操作")
	}

	// 构建搜索查询
	searchQuery, err := b.buildDeleteSearchQuery(timeInterval, rowIDs)
	if err != nil {
		return 0, err
	}

	// 执行搜索以找到要删除的文档
	searchRequest := bleve.NewSearchRequest(searchQuery)
	searchRequest.Size = 10000 // 一次删除操作的最多文档数
	searchRequest.Fields = []string{"*"}

	searchResult, err := index.Search(searchRequest)
	if err != nil {
		return 0, fmt.Errorf("搜索要删除的文档失败: %v", err)
	}

	// 对匹配的文档进行软删除
	deletedCount := b.performSoftDeleteOnDocuments(ctx, index, searchResult.Hits)

	log.InfoContextf(ctx, "时序数据软删除完成，删除了%d条记录", deletedCount)
	return deletedCount, nil
}

// buildDeleteSearchQuery 构建删除操作的搜索查询
func (b *Bleve) buildDeleteSearchQuery(timeInterval *pb.TimeInterval, rowIDs []string) (query.Query, error) {
	if len(rowIDs) > 0 {
		// 按行ID删除
		return bleve.NewDocIDQuery(rowIDs), nil
	}

	if timeInterval != nil {
		// 按时间区间删除
		var queries []query.Query

		// 添加未删除条件
		deletedQuery := bleve.NewTermQuery("0")
		deletedQuery.SetField("_deleted")
		queries = append(queries, deletedQuery)

		// 添加时间范围条件
		timeQuery, err := b.buildTimeIntervalQuery(timeInterval)
		if err != nil {
			return nil, err
		}
		if timeQuery != nil {
			queries = append(queries, timeQuery)
		}

		if len(queries) > 1 {
			return bleve.NewConjunctionQuery(queries...), nil
		} else if len(queries) == 1 {
			return queries[0], nil
		}
	}

	return nil, fmt.Errorf("没有指定删除条件")
}

// deleteStaticData 删除静态数据（软删除）
func (b *Bleve) deleteStaticData(ctx context.Context, index bleve.Index, rowIDs []string) (uint64, error) {
	if index == nil {
		return 0, fmt.Errorf("索引为空，无法执行删除操作")
	}
	if len(rowIDs) == 0 {
		return 0, fmt.Errorf("静态数据删除必须指定行ID")
	}

	// 构建查询
	searchQuery := bleve.NewDocIDQuery(rowIDs)

	// 执行搜索
	searchRequest := bleve.NewSearchRequest(searchQuery)
	searchRequest.Size = 10000 // 设置一个较大的值以获取所有匹配的文档
	searchRequest.Fields = []string{"*"}

	searchResults, err := index.Search(searchRequest)
	if err != nil {
		log.ErrorContextf(ctx, "搜索要删除的文档失败: %v", err)
		return 0, fmt.Errorf("搜索要删除的文档失败: %v", err)
	}

	// 执行软删除
	deletedCount := b.performSoftDeleteOnDocuments(ctx, index, searchResults.Hits)
	log.InfoContextf(ctx, "静态数据软删除完成，删除了%d条记录", deletedCount)
	return deletedCount, nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// buildTimeIntervalQuery 构建时间区间查询
func (b *Bleve) buildTimeIntervalQuery(timeInterval *pb.TimeInterval) (query.Query, error) {
	if timeInterval.GetStart() == "" && timeInterval.GetEnd() == "" {
		return nil, nil
	}

	var minTime, maxTime *string
	if timeInterval.GetStart() != "" {
		start := timeInterval.GetStart()
		minTime = &start
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

	timeQuery := bleve.NewDateRangeQuery(minTimeObj, maxTimeObj)
	setQueryField(timeQuery, "_times")
	return timeQuery, nil
}

// performSoftDeleteOnDocuments 对文档执行软删除操作
func (b *Bleve) performSoftDeleteOnDocuments(ctx context.Context, index bleve.Index, hits []*search.DocumentMatch) uint64 {
	var deletedCount uint64
	deleteTime := time.Now().Format("2006-01-02 15:04:05")

	for _, hit := range hits {
		// 创建软删除的文档数据
		docData := make(map[string]interface{})

		// 从搜索结果中获取字段数据
		for fieldName, fieldValue := range hit.Fields {
			docData[fieldName] = fieldValue
		}

		// 设置删除标记和删除时间
		docData["_deleted"] = "1"
		docData["_deleted_time"] = deleteTime

		// 重新索引文档（实现软删除）
		err := index.Index(hit.ID, docData)
		if err != nil {
			log.WarnContextf(ctx, "软删除文档[%s]失败: %v", hit.ID, err)
			continue
		}
		deletedCount++
	}

	return deletedCount
}
