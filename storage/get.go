package storage

import (
	"context"
	"fmt"
	"time"

	pb "github.com/mooyang-code/xData-mini/storage/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

// DataRow 数据行
type DataRow struct {
	Times  string
	RowId  string
	Fields map[string]*ReadFieldValue
}

// ReadFieldValue 字段值
type ReadFieldValue struct {
	FieldKey   string
	FieldType  int
	StrValue   string
	IntValue   int64
	FloatValue float64
}

// ReadResponse 读取响应结果
type ReadResponse struct {
	Success    bool
	ErrorMsg   string
	DataRows   []*DataRow
	RowsRead   int
	FailedRows []string
}

// GetData 从存储服务读取数据
// 参数:
//   - projectID: 项目ID
//   - datasetID: 数据集ID
//   - objectID: 数据对象ID
//   - freq: 时序频率
//   - startTime: 开始时间（选填）
//   - endTime: 结束时间（选填）
//   - rowID: 行ID（选填）
//   - maxLimit: 最大返回行数（默认1000）
//
// 返回: 读取结果
func (s *StorageOperator) GetData(projectID int, datasetID int, objectID string, freq string,
	startTime string, endTime string, rowID string, maxLimit uint32) (*ReadResponse, error) {
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

	// 如果未设置最大返回行数，使用默认值
	if maxLimit == 0 {
		maxLimit = 1000
	}

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
	if startTime != "" || endTime != "" {
		timeRange = &pb.TimeRange{
			Start: startTime,
		}
		if endTime != "" {
			timeRange.RangeType = &pb.TimeRange_End{
				End: endTime,
			}
		}
	}

	// 创建GetData请求
	dataParam := &pb.GetDataParams{
		DataKey:   pbDataKey,
		TimeRange: timeRange,
		RowId:     rowID,
		MaxLimit:  maxLimit,
	}
	req := &pb.GetDataReq{
		AuthInfo: &pb.AuthInfo{
			AppId:  "test123",
			AppKey: "test123",
		},
		DataParams: []*pb.GetDataParams{dataParam},
	}

	// 设置请求超时
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 发送请求
	rsp, err := c.GetData(ctx, req)
	if err != nil {
		return &ReadResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("发送请求失败: %v", err),
		}, nil
	}

	// 处理响应
	if rsp.RetInfo.Code != 0 {
		return &ReadResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("请求失败: %s (代码: %d)", rsp.RetInfo.Msg, rsp.RetInfo.Code),
		}, nil
	}

	// 处理返回的数据
	result := &ReadResponse{
		Success:    rsp.RetInfo.Code == 0,
		ErrorMsg:   rsp.RetInfo.Msg,
		DataRows:   []*DataRow{},
		FailedRows: []string{},
	}

	// 生成数据键字符串（用于从map中获取数据）
	dataKeyStr := fmt.Sprintf("%d_%d_%s_%s", projectID, datasetID, objectID, freq)

	// 处理成功数据
	if dataList, exists := rsp.DataList[dataKeyStr]; exists && dataList != nil {
		for _, row := range dataList.DataRows {
			dataRow := &DataRow{
				Times:  row.Times,
				RowId:  row.RowId,
				Fields: make(map[string]*ReadFieldValue),
			}

			for fieldName, field := range row.Fields {
				fieldValue := &ReadFieldValue{
					FieldKey:  field.FieldKey,
					FieldType: int(field.FieldType),
				}

				// 根据字段类型提取值
				if field.SimpleValue != nil {
					switch v := field.SimpleValue.Value.(type) {
					case *pb.SimpleValue_Str:
						fieldValue.StrValue = v.Str
					case *pb.SimpleValue_Int:
						fieldValue.IntValue = v.Int
					case *pb.SimpleValue_Float:
						fieldValue.FloatValue = v.Float
					}
				}

				dataRow.Fields[fieldName] = fieldValue
			}

			result.DataRows = append(result.DataRows, dataRow)
		}
	}

	// 处理失败数据
	if failedList, exists := rsp.FailedList[dataKeyStr]; exists && failedList != nil {
		for _, row := range failedList.DataRows {
			result.FailedRows = append(result.FailedRows, row.RowId)
		}
	}

	result.RowsRead = len(result.DataRows)
	return result, nil
}
