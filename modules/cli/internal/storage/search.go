package storage

import (
	"context"
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

// 用于封装搜索结果的结构
type SearchResponse struct {
	Success      bool
	ErrorMsg     string
	TotalResults uint64
	DataRows     []*pb.DataRow
	FailedFields map[string]string
}

// SearchData 向存储服务发送搜索请求
// 参数:
//   - projectID: 项目ID
//   - datasetID: 数据集ID
//   - objectID: 数据对象ID
//   - freq: 时序频率
//   - startTime: 起始时间 (可选，格式: "YYYY-MM-DD HH:MM:SS")
//   - endTime: 结束时间 (可选，格式: "YYYY-MM-DD HH:MM:SS")
//   - dryRun: 是否仅显示请求不实际发送
//
// 返回: 搜索结果
func (s *StorageOperator) SearchData(projectID int, datasetID int, objectID string, freq string, startTime, endTime string) (*SearchResponse, error) {
	if objectID == "" {
		return nil, fmt.Errorf("数据对象ID不能为空")
	}
	if s.Config == nil {
		return nil, fmt.Errorf("存储服务配置不存在")
	}
	target := s.Config.Storage.Target
	if target == "" {
		return nil, fmt.Errorf("存储服务地址未配置")
	}

	// 构建搜索选项
	options := createTestSearchOptions()

	// 创建客户端连接
	c := pb.NewAccessClientProxy(client.WithTarget("ip://" + target))

	// 创建proto的DataKey
	pbDataKey := &pb.DataKey{
		ProjectId: int32(projectID),
		DatasetId: int32(datasetID),
		ObjectId:  objectID,
		Freq:      freq,
	}

	// 创建时间范围
	var timeRange *pb.TimeRange
	if startTime != "" {
		timeRange = &pb.TimeRange{
			Start: startTime,
		}
		if endTime != "" {
			timeRange.RangeType = &pb.TimeRange_End{End: endTime}
		}
	}

	// 创建分页信息
	pageInfo := &pb.PageInfo{
		PageIdx: 1,
		Size:    10,
	}

	// 创建SearchData请求
	req := &pb.SearchDataReq{
		AuthInfo: &pb.AuthInfo{
			AppId:  "",
			AppKey: "",
		},
		DataKey:   pbDataKey,
		TimeRange: timeRange,
		Options:   options,
		PageInfo:  pageInfo,
	}

	// 设置请求超时
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 发送请求
	rsp, err := c.SearchData(ctx, req)
	if err != nil {
		return &SearchResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("发送请求失败: %v", err),
		}, nil
	}

	// 处理响应
	if rsp.RetInfo.Code != 0 {
		return &SearchResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("请求失败: %s (代码: %d)", rsp.RetInfo.Msg, rsp.RetInfo.Code),
		}, nil
	}

	// 处理失败的字段
	failedFields := make(map[string]string)
	if len(rsp.FailedFields) > 0 {
		for fieldName, failInfo := range rsp.FailedFields {
			failedFields[fieldName] = failInfo.Msg
		}
	}

	return &SearchResponse{
		Success:      rsp.RetInfo.Code == 0,
		ErrorMsg:     rsp.RetInfo.Msg,
		TotalResults: rsp.Total,
		DataRows:     rsp.DataRows,
		FailedFields: failedFields,
	}, nil
}

// 创建测试搜索选项
func createTestSearchOptions() *pb.Options {
	// 创建一个示例搜索条件
	return &pb.Options{
		// 创建条件组
		CondGroups: []*pb.CondGroup{
			{
				// 第一个条件组: 价格相关条件
				Conds: []*pb.Cond{
					{
						FieldKey: "close",
						Op:       pb.Operator_lt,
						Value: &pb.SimpleValue{
							Value: &pb.SimpleValue_Float{Float: 3000.0},
						},
					},
					{
						FieldKey: "volume",
						Op:       pb.Operator_gt,
						Value: &pb.SimpleValue{
							Value: &pb.SimpleValue_Float{Float: 100.0},
						},
					},
				},
				Logical: pb.Logical_LogicalAnd, // 条件之间是AND关系
			},
			{
				// 第二个条件组: 标记相关条件
				Conds: []*pb.Cond{
					{
						FieldKey: "taker_buy_quote_asset_volume",
						Op:       pb.Operator_lt,
						Value: &pb.SimpleValue{
							Value: &pb.SimpleValue_Float{Float: 3800000.0},
						},
					},
				},
				Logical: pb.Logical_LogicalAnd, // 在该组内部使用AND (虽然这里只有一个条件)
			},
		},
		Logical: pb.Logical_LogicalAnd,
		// 按close字段降序排序
		Sort: []*pb.SortInfo{
			{
				FieldKey: "close",
				Sort:     pb.Sort_Desc,
			},
		},
		// 指定返回的字段
		Includes: []string{
			"candle_begin_time",
			"open",
			"high",
			"low",
			"close",
			"volume",
		},
		// 最大记录数
		MaxNum: 100,
	}
}
