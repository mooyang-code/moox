package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

// 字段类型常量
const (
	StrField    = 1
	IntField    = 2
	FloatField  = 3
	TimeField   = 4
	IntVecField = 5
	SetField    = 6
	MapKVField  = 7
)

// 更新类型常量
const (
	SetUpdate    = 1
	DelUpdate    = 2
	AppendUpdate = 3
)

// DataKey 数据键
type DataKey struct {
	ProjectId int32
	DatasetId int32
	ObjectId  string
	Freq      string
}

// 字段值
type FieldValue struct {
	Value any
}

// 更新字段
type UpdateField struct {
	FieldKey   string
	FieldType  int
	UpdateType int
	StrValue   string
	IntValue   int64
	FloatValue float64
}

// 数据行
type UpdateDataRow struct {
	Times  string
	RowId  string
	Fields map[string]*UpdateField
}

// WriteResponse 写入响应结果
type WriteResponse struct {
	Success     bool
	ErrorMsg    string
	FailedRows  []string
	RowsWritten int
}

// StorageOperator 存储操作类
type StorageOperator struct {
	Config *config.Config // 配置
}

// NewStorageOperator 创建存储操作类实例
func NewStorageOperator(config *config.Config) *StorageOperator {
	return &StorageOperator{
		Config: config,
	}
}

// SetData 向存储服务写入数据
// 参数:
//   - projectID: 项目ID
//   - datasetID: 数据集ID
//   - objectID: 数据对象ID
//   - freq: 时序频率
//   - dryRun: 是否仅显示数据不实际写入
//
// 返回: 写入结果
func (s *StorageOperator) SetData(projectID int, datasetID int, objectID string, freq string, dryRun bool) (*WriteResponse, error) {
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

	// 创建测试数据行
	dataRows := createTestDataRows()

	// 如果是演示模式，只打印信息不发送请求
	if dryRun {
		fmt.Printf("将连接到存储服务: %s\n", target)
		fmt.Printf("使用数据键: 项目ID=%d, 数据集ID=%d, 对象ID=%s, 频率=%s\n",
			projectID, datasetID, objectID, freq)
		fmt.Printf("将要写入 %d 行数据\n", len(dataRows))

		for i, row := range dataRows {
			fmt.Printf("  行 %d: 时间=%s, 行ID=%s\n", i+1, row.Times, row.RowId)
			for fieldName, field := range row.Fields {
				var valueStr string
				switch field.FieldType {
				case StrField:
					valueStr = field.StrValue
				case IntField:
					valueStr = fmt.Sprintf("%d", field.IntValue)
				case FloatField:
					valueStr = fmt.Sprintf("%f", field.FloatValue)
				default:
					valueStr = "<复杂类型>"
				}
				fmt.Printf("    字段: %s = %s\n", fieldName, valueStr)
			}
		}

		fmt.Println("\n注意: 此次为演示模式，不会实际发送请求。")
		return &WriteResponse{
			Success:     true,
			RowsWritten: len(dataRows),
		}, nil
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

	// 转换数据行为proto格式
	pbDataRows := make([]*pb.UpdateDataRow, 0, len(dataRows))
	for _, row := range dataRows {
		pbRow := &pb.UpdateDataRow{
			Times:  row.Times,
			RowId:  row.RowId,
			Fields: make(map[string]*pb.UpdateField),
		}

		for fieldName, field := range row.Fields {
			pbField := &pb.UpdateField{
				FieldKey:   field.FieldKey,
				FieldType:  pb.EnumFieldType(field.FieldType),
				UpdateType: pb.EnumUpdateType(field.UpdateType),
			}

			// 根据字段类型设置值
			switch field.FieldType {
			case StrField:
				pbField.SimpleValue = &pb.SimpleValue{
					Value: &pb.SimpleValue_Str{
						Str: field.StrValue,
					},
				}
			case IntField:
				pbField.SimpleValue = &pb.SimpleValue{
					Value: &pb.SimpleValue_Int{
						Int: field.IntValue,
					},
				}
			case FloatField:
				pbField.SimpleValue = &pb.SimpleValue{
					Value: &pb.SimpleValue_Float{
						Float: field.FloatValue,
					},
				}
			}

			pbRow.Fields[fieldName] = pbField
		}

		pbDataRows = append(pbDataRows, pbRow)
	}

	// 创建SetData请求
	req := &pb.SetDataReq{
		AuthInfo: &pb.AuthInfo{
			AppId:  "",
			AppKey: "",
		},
		DataList: []*pb.UpdateDataList{
			{
				DataKey:  pbDataKey,
				DataRows: pbDataRows,
			},
		},
	}

	// 设置请求超时
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 发送请求
	rsp, err := c.SetData(ctx, req)
	if err != nil {
		return &WriteResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("发送请求失败: %v", err),
		}, nil
	}

	// 处理响应
	if rsp.RetInfo.Code != 0 {
		return &WriteResponse{
			Success:  false,
			ErrorMsg: fmt.Sprintf("请求失败: %s (代码: %d)", rsp.RetInfo.Msg, rsp.RetInfo.Code),
		}, nil
	}

	// 检查是否有失败的数据
	failedRows := []string{}
	if len(rsp.FailedList) > 0 {
		for key, failedList := range rsp.FailedList {
			for _, row := range failedList.DataRows {
				failedRows = append(failedRows, fmt.Sprintf("%s:%s", key, row.RowId))
			}
		}
	}

	return &WriteResponse{
		Success:     rsp.RetInfo.Code == 0,
		ErrorMsg:    rsp.RetInfo.Msg,
		FailedRows:  failedRows,
		RowsWritten: len(dataRows) - len(failedRows),
	}, nil
}

// GetTestData 返回测试数据，供上层应用使用
func (s *StorageOperator) GetTestData() []*UpdateDataRow {
	return createTestDataRows()
}

// 创建测试数据行
func createTestDataRows() []*UpdateDataRow {
	return []*UpdateDataRow{
		{
			Times: "2024-05-01 10:00:00",
			RowId: "BTCUSDT:2024-05-01 10:00:00",
			Fields: map[string]*UpdateField{
				"symbol": {
					FieldKey:   "symbol",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "BTCUSDT",
				},
				"unshelve_time": {
					FieldKey:   "unshelve_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2099-01-01 00:00:00",
				},
				"candle_begin_time": {
					FieldKey:   "candle_begin_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2024-05-01 10:00:00",
				},
				"open": {
					FieldKey:   "open",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 42789.50,
				},
				"high": {
					FieldKey:   "high",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 42890.25,
				},
				"low": {
					FieldKey:   "low",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 42680.75,
				},
				"close": {
					FieldKey:   "close",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 42750.50,
				},
				"volume": {
					FieldKey:   "volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 156.78912345,
				},
				"quote_volume": {
					FieldKey:   "quote_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 6700123.45,
				},
				"trade_num": {
					FieldKey:   "trade_num",
					FieldType:  IntField,
					UpdateType: SetUpdate,
					IntValue:   3456,
				},
				"taker_buy_base_asset_volume": {
					FieldKey:   "taker_buy_base_asset_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 89.12345678,
				},
				"taker_buy_quote_asset_volume": {
					FieldKey:   "taker_buy_quote_asset_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 3800123.45,
				},
				"candle_end_time": {
					FieldKey:   "candle_end_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2024-05-01 10:59:59",
				},
			},
		},
		{
			Times: "2024-05-01 11:00:00",
			RowId: "ETHUSDT:2024-05-01 11:00:00",
			Fields: map[string]*UpdateField{
				"symbol": {
					FieldKey:   "symbol",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "ETHUSDT",
				},
				"unshelve_time": {
					FieldKey:   "unshelve_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2099-01-01 00:00:00",
				},
				"candle_begin_time": {
					FieldKey:   "candle_begin_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2024-05-01 11:00:00",
				},
				"open": {
					FieldKey:   "open",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 2345.75,
				},
				"high": {
					FieldKey:   "high",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 2367.50,
				},
				"low": {
					FieldKey:   "low",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 2328.25,
				},
				"close": {
					FieldKey:   "close",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 2352.80,
				},
				"volume": {
					FieldKey:   "volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 789.45678901,
				},
				"quote_volume": {
					FieldKey:   "quote_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 1850456.78,
				},
				"trade_num": {
					FieldKey:   "trade_num",
					FieldType:  IntField,
					UpdateType: SetUpdate,
					IntValue:   2789,
				},
				"taker_buy_base_asset_volume": {
					FieldKey:   "taker_buy_base_asset_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 423.78901234,
				},
				"taker_buy_quote_asset_volume": {
					FieldKey:   "taker_buy_quote_asset_volume",
					FieldType:  FloatField,
					UpdateType: SetUpdate,
					FloatValue: 998765.43,
				},
				"candle_end_time": {
					FieldKey:   "candle_end_time",
					FieldType:  StrField,
					UpdateType: SetUpdate,
					StrValue:   "2024-05-01 11:59:59",
				},
			},
		},
	}
}
