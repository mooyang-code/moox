// Package common 提供通用的API响应格式和工具函数
package common

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// UnifiedAPIResponse 统一的API响应格式 - 兼容wuji格式并支持标准API（wuji即为apicache）
type UnifiedAPIResponse struct {
	Code    int    `json:"code"`    // 状态码（200表示成功）
	Message string `json:"message"` // 返回消息
	Data    []any  `json:"data"`    // 数据数组（wuji格式要求）
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

// ErrorResponse 错误响应
func ErrorResponse(c *gin.Context, statusCode int, message string, err error) {
	response := &UnifiedAPIResponse{
		Code:    statusCode,
		Message: message,
		Data:    []any{},
	}

	// 在开发模式下包含错误详情
	if err != nil {
		log.Errorf("API Error: %s - %v", message, err)
		if gin.Mode() == gin.DebugMode {
			response.Data = []any{map[string]string{
				"error": err.Error(),
			}}
		}
	}

	c.JSON(statusCode, response)
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
		for i, v := range arr {
			result[i] = v
		}
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

// PaginatedResponse 分页响应
func PaginatedResponse(c *gin.Context, message string, items interface{}, total int64, page, pageSize int) {
	data := map[string]interface{}{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  int64(page*pageSize) < total,
	}
	SuccessResponse(c, message, data)
}

// ListResponse 列表响应（将列表项直接作为data数组）
func ListResponse(c *gin.Context, message string, items interface{}) {
	SuccessResponse(c, message, items)
}
