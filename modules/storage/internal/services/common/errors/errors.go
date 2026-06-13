// Package errors 提供现代化的错误处理工具
package errors

import (
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// 错误消息映射表
var errorMessages = map[pb.EnumErrorCode]string{
	pb.EnumErrorCode_SUCCESS:               "操作成功",
	pb.EnumErrorCode_INVALID_PARAM:         "请求参数错误",
	pb.EnumErrorCode_NO_APP:                "App不存在",
	pb.EnumErrorCode_NO_AUTH:               "当前操作无权限",
	pb.EnumErrorCode_OVER_APPLY_REQ:        "请求超载，请稍后重试",
	pb.EnumErrorCode_NO_APP_FIELD:          "字段配置不存在",
	pb.EnumErrorCode_NO_PERMISSION:         "无字段操作权限",
	pb.EnumErrorCode_FIELD_INFO_NOT_EXIST:  "字段不存在",
	pb.EnumErrorCode_NO_ROUTE_CFG:          "路由配置不存在",
	pb.EnumErrorCode_INVALID_OP_TYPE:       "数据操作类型非法",
	pb.EnumErrorCode_NO_ROUTE_STORE_ITEM:   "数据连接失败",
	pb.EnumErrorCode_NO_DEV_CFG:            "设备信息不存在",
	pb.EnumErrorCode_FAILED_CONNECT_DEV:    "连接存储设备失败",
	pb.EnumErrorCode_FAILED_SELECT:         "获取数据失败",
	pb.EnumErrorCode_FAILED_UPDATE_VEC:     "更新选项字段失败",
	pb.EnumErrorCode_FAILED_UPDATE_ALL:     "更新全部字段失败",
	pb.EnumErrorCode_FAILED_UPDATE:         "更新数据失败",
	pb.EnumErrorCode_INNER_ERR:             "内部错误",
	pb.EnumErrorCode_CALL_ADAPTER_ERR:      "请求第三方服务失败",
	pb.EnumErrorCode_NO_OP_RIGHT:           "没有操作权限",
	pb.EnumErrorCode_INVALID_DATA_SET:      "无效的数据集ID",
	pb.EnumErrorCode_NOT_SUPPORT:           "不支持当前操作",
	pb.EnumErrorCode_VALIDATE_FIELD_VALUES: "字段值校验失败",
}

// GetMessage 获取错误消息
func GetMessage(code pb.EnumErrorCode) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}
	return "未知错误"
}

// GetMessageWithDetail 获取包含详细信息的错误消息
func GetMessageWithDetail(code pb.EnumErrorCode, detail string) string {
	msg := GetMessage(code)
	if detail != "" {
		return fmt.Sprintf("%s: %s", msg, detail)
	}
	return msg
}

// NewError 创建业务错误
func NewError(code pb.EnumErrorCode, detail string) error {
	msg := GetMessageWithDetail(code, detail)
	return fmt.Errorf("%s", msg)
}

// NewErrorf 创建格式化的业务错误
func NewErrorf(code pb.EnumErrorCode, format string, args ...interface{}) error {
	detail := fmt.Sprintf(format, args...)
	return NewError(code, detail)
}

// GetErrMsg 向后兼容函数，建议使用 GetMessage
func GetErrMsg(code pb.EnumErrorCode, err error) string {
	return GetMessage(code)
}
