// Package common 提供通用的API响应格式和工具函数
package common

import (
	"errors"
	"net/http"
	"reflect"

	apperrors "github.com/mooyang-code/moox/modules/control/internal/errors"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// UnifiedAPIResponse 统一的API响应格式 - 兼容wuji格式并支持标准API（wuji即为apicache）
type UnifiedAPIResponse struct {
	Code    int    `json:"code"`            // 状态码（200表示成功）
	Message string `json:"message"`         // 返回消息
	Data    []any  `json:"data"`            // 数据数组（wuji格式要求）
	Total   *int64 `json:"total,omitempty"` // 总数（分页列表时使用）
}

// SuccessResponse 成功响应
func SuccessResponse(c *gin.Context, message string, data interface{}) {
	response := &UnifiedAPIResponse{
		Code:    200,
		Message: message,
		Data:    convertToArray(data),
	}
	c.JSON(http.StatusOK, response)
}

// HandleAppError 处理AppError并返回统一格式的响应
func HandleAppError(c *gin.Context, err error) {
	if err == nil {
		return
	}

	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		// 记录错误日志
		if appErr.HTTPStatus >= 500 {
			log.ErrorContextf(c.Request.Context(), "Server error: %v", appErr)
		} else {
			log.WarnContextf(c.Request.Context(), "Client error: %v", appErr)
		}

		// 构建响应
		response := &UnifiedAPIResponse{
			Code:    appErr.HTTPStatus,
			Message: appErr.Message,
			Data:    []any{},
		}

		// 开发模式下包含详细信息
		if gin.Mode() == gin.DebugMode && appErr.Details != nil && len(appErr.Details) > 0 {
			response.Data = []any{appErr.Details}
		}

		c.JSON(appErr.HTTPStatus, response)
		return
	}

	// 未知错误
	log.ErrorContextf(c.Request.Context(), "Unknown error: %v", err)
	c.JSON(http.StatusInternalServerError, &UnifiedAPIResponse{
		Code:    http.StatusInternalServerError,
		Message: "Internal server error",
		Data:    []any{},
	})
}

// convertToArray 将任意数据转换为数组格式
func convertToArray(data interface{}) []any {
	if data == nil {
		return nil
	}

	// 如果已经是数组，直接返回
	if arr, ok := data.([]any); ok {
		// 如果是空数组，返回nil而不是空数组
		if len(arr) == 0 {
			return nil
		}
		return arr
	}

	// 如果是[]interface{}，转换为[]any
	if arr, ok := data.([]interface{}); ok {
		// 如果是空数组，返回nil而不是空数组
		if len(arr) == 0 {
			return nil
		}
		result := make([]any, len(arr))
		copy(result, arr)
		return result
	}

	// 检查是否是切片类型
	switch v := data.(type) {
	case []string:
		// 如果是空数组，返回nil而不是空数组
		if len(v) == 0 {
			return nil
		}
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = item
		}
		return result
	case []int:
		// 如果是空数组，返回nil而不是空数组
		if len(v) == 0 {
			return nil
		}
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = item
		}
		return result
	case []map[string]interface{}:
		// 如果是空数组，返回nil而不是空数组
		if len(v) == 0 {
			return nil
		}
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = item
		}
		return result
	}

	// 使用反射处理其他切片类型
	if isSlice(data) {
		return convertSliceToArray(data)
	}

	// 其他情况，包装成单元素数组
	return []any{data}
}

// isSlice 判断是否为切片类型
func isSlice(data interface{}) bool {
	if data == nil {
		return false
	}
	v := reflect.ValueOf(data)
	return v.Kind() == reflect.Slice
}

// convertSliceToArray 使用反射将切片转换为数组
func convertSliceToArray(data interface{}) []any {
	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Slice {
		return []any{data}
	}

	// 如果是空切片，返回nil而不是空数组
	if v.Len() == 0 {
		return nil
	}

	result := make([]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		result[i] = v.Index(i).Interface()
	}
	return result
}

// PaginatedListResponse 分页列表响应（新格式：total在外层，items直接作为data数组）
func PaginatedListResponse(c *gin.Context, message string, items interface{}, total int64) {
	response := &UnifiedAPIResponse{
		Code:    200,
		Message: message,
		Data:    convertToArray(items),
		Total:   &total,
	}
	c.JSON(http.StatusOK, response)
}
