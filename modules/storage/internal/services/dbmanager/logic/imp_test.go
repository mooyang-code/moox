package logic

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestDetermineDataType(t *testing.T) {
	// 创建测试实例
	impl := &DBTableManagerServiceImpl{}

	tests := []struct {
		name     string
		dataKey  *pb.DataKey
		expected pb.EnumDataTypeCategory
	}{
		{
			name: "时序数据 - Freq非空",
			dataKey: &pb.DataKey{
				ProjectId: 1,
				DatasetId: 1,
				ObjectId:  "test_object",
				Freq:      "1D", // 非空频率
			},
			expected: pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		},
		{
			name: "静态数据 - Freq为空",
			dataKey: &pb.DataKey{
				ProjectId: 1,
				DatasetId: 1,
				ObjectId:  "test_object",
				Freq:      "", // 空频率
			},
			expected: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		},
		{
			name: "时序数据 - 分钟频率",
			dataKey: &pb.DataKey{
				ProjectId: 1,
				DatasetId: 1,
				ObjectId:  "test_object",
				Freq:      "5m", // 5分钟频率
			},
			expected: pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := impl.determineDataType(tt.dataKey)
			if result != tt.expected {
				t.Errorf("determineDataType() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestGenerateTableName(t *testing.T) {
	// 创建测试实例
	impl := &DBTableManagerServiceImpl{}

	dataKey := &pb.DataKey{
		ProjectId: 1,
		DatasetId: 100,
		ObjectId:  "test_object",
		Freq:      "1D",
	}

	tests := []struct {
		name       string
		customName *string
		tableType  pb.EnumTableType
		expected   string
	}{
		{
			name:       "自定义表名",
			customName: stringPtr("custom_table_name"),
			tableType:  pb.EnumTableType_DATA_TABLE,
			expected:   "custom_table_name",
		},
		{
			name:       "数据对象表",
			customName: nil,
			tableType:  pb.EnumTableType_DATA_OBJECT_TABLE,
			expected:   "t_object_100", // GenObjectTableID(100)
		},
		{
			name:       "数据表",
			customName: nil,
			tableType:  pb.EnumTableType_DATA_TABLE,
			expected:   "t_data_100_test_object_1D", // GenDataTableID(100, "test_object", "1D")
		},
		{
			name:       "未知表类型使用默认",
			customName: nil,
			tableType:  pb.EnumTableType_INVALID_TABLE_TYPE,
			expected:   "t_data_100_test_object_1D", // 默认使用数据表命名规则
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := impl.generateTableName(dataKey, tt.customName, tt.tableType)
			if result != tt.expected {
				t.Errorf("generateTableName() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// 辅助函数：创建字符串指针
func stringPtr(s string) *string {
	return &s
}
