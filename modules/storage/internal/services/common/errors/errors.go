// Package errors 提供现代化的错误处理工具
package errors

import (
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// 错误消息映射表
var errorMessages = map[pb.ErrorCode]string{
	pb.ErrorCode_SUCCESS:                        "操作成功",
	pb.ErrorCode_INVALID_PARAM:                  "请求参数错误",
	pb.ErrorCode_NO_AUTH:                        "未认证或认证失败",
	pb.ErrorCode_NO_PERMISSION:                  "没有操作权限",
	pb.ErrorCode_INNER_ERR:                      "内部错误",
	pb.ErrorCode_WORKSPACE_NOT_FOUND:            "工作空间不存在",
	pb.ErrorCode_DATASET_NOT_FOUND:              "数据集不存在",
	pb.ErrorCode_INSTRUMENT_NOT_FOUND:           "标的不存在",
	pb.ErrorCode_FIELD_NOT_FOUND:                "字段不存在",
	pb.ErrorCode_FACTOR_INSTANCE_NOT_FOUND:      "因子实例不存在",
	pb.ErrorCode_DATA_VIEW_NOT_READY:            "数据视图尚未就绪",
	pb.ErrorCode_DATA_VIEW_COLUMN_NOT_FOUND:     "数据视图列不存在",
	pb.ErrorCode_QUERY_SHAPE_UNSUPPORTED:        "不支持当前查询形态",
	pb.ErrorCode_ROUTE_NOT_FOUND:                "未找到存储路由",
	pb.ErrorCode_ROUTE_CROSS_DEVICE_UNSUPPORTED: "不支持跨设备路由",
	pb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED:  "存储引擎不支持该能力",
	pb.ErrorCode_DIMENSION_VALUE_INVALID:        "业务维度取值不合法",
}

// GetMessage 获取错误消息
func GetMessage(code pb.ErrorCode) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}
	return "未知错误"
}

// GetMessageWithDetail 获取包含详细信息的错误消息
func GetMessageWithDetail(code pb.ErrorCode, detail string) string {
	msg := GetMessage(code)
	if detail != "" {
		return fmt.Sprintf("%s: %s", msg, detail)
	}
	return msg
}

// NewError 创建业务错误
func NewError(code pb.ErrorCode, detail string) error {
	msg := GetMessageWithDetail(code, detail)
	return fmt.Errorf("%s", msg)
}

// NewErrorf 创建格式化的业务错误
func NewErrorf(code pb.ErrorCode, format string, args ...interface{}) error {
	detail := fmt.Sprintf(format, args...)
	return NewError(code, detail)
}

// GetErrMsg 向后兼容函数，建议使用 GetMessage
func GetErrMsg(code pb.ErrorCode, err error) string {
	return GetMessage(code)
}
