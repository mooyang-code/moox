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
	"github.com/blevesearch/bleve/v2/search/query"
)

// GetFieldInfos 统一获取数据接口，Bleve支持静态数据和时序数据存储
func (b *Bleve) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	// 获取或创建索引
	indexPath := b.getTableIndexPath(params.TableID)
	index, err := getIndex(ctx, indexPath)
	if err != nil {
		return nil, err
	}
	if index == nil {
		return nil, fmt.Errorf("索引为空，无法执行查询操作")
	}
	// 操作完成后关闭索引连接
	defer index.Close()
	tableID := params.TableID
	maxLimit := params.MaxLimit

	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return b.GetTimingFieldInfos(ctx, tableID, params.TimeInterval, params.FieldIDs, params.MapKeys, maxLimit, index)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return b.GetStaticFieldInfos(ctx, tableID, params.RowID, params.FieldIDs, params.MapKeys, maxLimit, index)
	default:
		return nil, fmt.Errorf("invalid data type")
	}
}

// GetStaticFieldInfos 静态数据：统一获取value接口
func (b *Bleve) GetStaticFieldInfos(ctx context.Context, tableID string, rowID string,
	fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList, maxLimit uint32, index bleve.Index) ([]*pb.DocRow, error) {
	b.data = make(map[string]any)
	b.isGetAll = len(fieldIDs) == 0 || (len(fieldIDs) == 1 && fieldIDs[0] == 0)

	var q query.Query
	if rowID == "" {
		// 获取所有未删除的文档
		q = b.buildQuery(bleve.NewMatchAllQuery())
	} else {
		// 精确匹配文档ID，并确保未被删除
		docIDQuery := bleve.NewDocIDQuery([]string{rowID})
		q = b.buildQuery(docIDQuery)
	}

	// 使用共享函数执行搜索
	var sortFields []string // 静态数据无需排序
	return b.executeSearch(ctx, q, fieldIDs, maxLimit, sortFields, index)
}

// GetTimingFieldInfos 时序数据：统一获取value接口
func (b *Bleve) GetTimingFieldInfos(ctx context.Context, tableID string, timeInterval *pb.TimeInterval,
	fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList, maxLimit uint32, index bleve.Index) ([]*pb.DocRow, error) {
	b.data = make(map[string]any)
	b.isGetAll = len(fieldIDs) == 0 || (len(fieldIDs) == 1 && fieldIDs[0] == 0)

	// 构建时间范围查询
	var baseQuery query.Query
	if timeInterval == nil || (timeInterval.Start == "" && timeInterval.GetEnd() == "") {
		// 没有时间限制，返回所有文档
		baseQuery = bleve.NewMatchAllQuery()
	} else {
		// 创建时间范围查询
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

		// 创建时间范围查询
		timeQuery := bleve.NewDateRangeQuery(minTimeObj, maxTimeObj)
		timeQuery.SetField("_times")
		baseQuery = timeQuery
	}

	// 添加未删除条件
	finalQuery := b.buildQuery(baseQuery)

	// 时序数据按时间排序
	sortFields := []string{"_times"}
	return b.executeSearch(ctx, finalQuery, fieldIDs, maxLimit, sortFields, index)
}

// executeSearch 执行搜索并处理结果的共享函数
func (b *Bleve) executeSearch(ctx context.Context, q query.Query,
	fieldIDs []uint32, maxLimit uint32, sortFields []string, index bleve.Index) ([]*pb.DocRow, error) {
	// 设置搜索请求
	searchRequest := bleve.NewSearchRequest(q)
	if maxLimit > 0 {
		searchRequest.Size = int(maxLimit)
	} else {
		searchRequest.Size = 100 // 默认返回100条
	}
	searchRequest.Fields = []string{"*"} // 返回所有存储的字段

	// 如果有排序字段，设置排序
	if len(sortFields) > 0 {
		searchRequest.SortBy(sortFields)
	}

	// 执行搜索
	searchResults, err := index.Search(searchRequest)
	if err != nil {
		log.ErrorContextf(ctx, "Bleve search error: %v", err)
		return nil, err
	}

	// 处理搜索结果
	var results []*pb.DocRow
	for _, hit := range searchResults.Hits {
		docRow := b.documentToDocRow(hit.Fields)

		// 过滤字段
		if !b.isGetAll && len(fieldIDs) > 0 {
			filteredFields := make(map[uint32]*pb.FieldInfo)
			for _, fieldID := range fieldIDs {
				if fieldInfo, ok := docRow.Fields[fieldID]; ok {
					filteredFields[fieldID] = fieldInfo
				}
			}
			docRow.Fields = filteredFields
		}
		results = append(results, docRow)
	}
	return results, nil
}
