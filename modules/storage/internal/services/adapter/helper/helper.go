package helper

import (
	"regexp"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/segmentio/ksuid"
)

// GenRowID 生成一个唯一的RowID
func GenRowID() string {
	return "rid" + ksuid.New().String()
}

// ParseTableType 根据tableID解析表类型
func ParseTableType(tableID string) pb.EnumTableType {
	if strings.HasPrefix(tableID, "t_object") {
		return pb.EnumTableType_DATA_OBJECT_TABLE
	} else if strings.HasPrefix(tableID, "t_data") {
		return pb.EnumTableType_DATA_TABLE
	}
	// 默认返回对象表类型
	return pb.EnumTableType_DATA_OBJECT_TABLE
}

var freqPattern = regexp.MustCompile(`^([1-9]\d*)([smHDWMY])$`)

// InferDataTypeFromTableID 根据表名格式推断数据类型
// t_object_* 视为静态数据；t_data_* 且末尾段匹配频率格式视为时序数据
func InferDataTypeFromTableID(tableID string) (pb.EnumDataTypeCategory, bool) {
	if strings.HasPrefix(tableID, "t_object_") {
		return pb.EnumDataTypeCategory_STATIC_DATA_TYPE, true
	}
	if !strings.HasPrefix(tableID, "t_data_") {
		return pb.EnumDataTypeCategory_INVALID_DATA_TYPE_CATEGORY, false
	}
	parts := strings.Split(tableID, "_")
	if len(parts) == 0 {
		return pb.EnumDataTypeCategory_INVALID_DATA_TYPE_CATEGORY, false
	}
	last := parts[len(parts)-1]
	if freqPattern.MatchString(last) {
		return pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE, true
	}
	return pb.EnumDataTypeCategory_STATIC_DATA_TYPE, true
}
