// Package errors 提供统一的错误处理机制
package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// Code 错误码类型
type Code string

// 系统级错误码
const (
	CodeSuccess      Code = "SUCCESS"
	CodeInternal     Code = "INTERNAL_ERROR"
	CodeInvalidParam Code = "INVALID_PARAM"
	CodeUnauthorized Code = "UNAUTHORIZED"
	CodeForbidden    Code = "FORBIDDEN"
	CodeNotFound     Code = "NOT_FOUND"
	CodeConflict     Code = "CONFLICT"
	CodeTimeout      Code = "TIMEOUT"
)

// 业务级错误码
const (
	CodeUserNotFound      Code = "USER_NOT_FOUND"
	CodeUserLocked        Code = "USER_LOCKED"
	CodePasswordIncorrect Code = "PASSWORD_INCORRECT"
	CodeVersionExists     Code = "VERSION_EXISTS"
	CodeNodeOffline       Code = "NODE_OFFLINE"
	CodeNodeNotFound      Code = "NODE_NOT_FOUND"
	CodeTaskNotFound      Code = "TASK_NOT_FOUND"
	CodeTaskFailed        Code = "TASK_FAILED"
	CodePackageNotFound   Code = "PACKAGE_NOT_FOUND"
	CodeAccountNotFound   Code = "ACCOUNT_NOT_FOUND"
	CodeInvalidConfig     Code = "INVALID_CONFIG"
)

// AppError 应用错误
type AppError struct {
	Code       Code              `json:"code"`
	Message    string            `json:"message"`
	Details    map[string]string `json:"details,omitempty"`
	HTTPStatus int               `json:"-"`
	Err        error             `json:"-"`
}

// Error 实现error接口
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 支持errors.Unwrap
func (e *AppError) Unwrap() error {
	return e.Err
}

// WithDetail 添加详细信息
func (e *AppError) WithDetail(key, value string) *AppError {
	if e.Details == nil {
		e.Details = make(map[string]string)
	}
	e.Details[key] = value
	return e
}

// New 创建新错误
func New(code Code, message string) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		HTTPStatus: getHTTPStatus(code),
	}
}

// Wrap 包装错误
func Wrap(err error, code Code, message string) *AppError {
	if err == nil {
		return nil
	}

	// 如果已经是AppError，保留原始错误链
	var appErr *AppError
	if errors.As(err, &appErr) {
		return &AppError{
			Code:       code,
			Message:    message,
			HTTPStatus: getHTTPStatus(code),
			Err:        err,
		}
	}

	return &AppError{
		Code:       code,
		Message:    message,
		HTTPStatus: getHTTPStatus(code),
		Err:        err,
	}
}

// 常用错误构造函数

// NotFound 资源未找到
func NotFound(resource string) *AppError {
	return &AppError{
		Code:       CodeNotFound,
		Message:    fmt.Sprintf("%s not found", resource),
		HTTPStatus: http.StatusNotFound,
	}
}

// InvalidParam 参数无效
func InvalidParam(field string, reason string) *AppError {
	return &AppError{
		Code:    CodeInvalidParam,
		Message: fmt.Sprintf("invalid parameter: %s", field),
		Details: map[string]string{
			"field":  field,
			"reason": reason,
		},
		HTTPStatus: http.StatusBadRequest,
	}
}

// Unauthorized 未授权
func Unauthorized(message string) *AppError {
	if message == "" {
		message = "unauthorized"
	}
	return &AppError{
		Code:       CodeUnauthorized,
		Message:    message,
		HTTPStatus: http.StatusUnauthorized,
	}
}

// Forbidden 禁止访问
func Forbidden(message string) *AppError {
	if message == "" {
		message = "forbidden"
	}
	return &AppError{
		Code:       CodeForbidden,
		Message:    message,
		HTTPStatus: http.StatusForbidden,
	}
}

// Internal 内部错误
func Internal(message string, err error) *AppError {
	return &AppError{
		Code:       CodeInternal,
		Message:    message,
		HTTPStatus: http.StatusInternalServerError,
		Err:        err,
	}
}

// Conflict 冲突错误
func Conflict(message string) *AppError {
	return &AppError{
		Code:       CodeConflict,
		Message:    message,
		HTTPStatus: http.StatusConflict,
	}
}

// getHTTPStatus 根据错误码获取HTTP状态码
func getHTTPStatus(code Code) int {
	switch code {
	case CodeSuccess:
		return http.StatusOK
	case CodeInvalidParam:
		return http.StatusBadRequest
	case CodeUnauthorized, CodePasswordIncorrect:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound, CodeUserNotFound, CodeNodeNotFound, CodeTaskNotFound, CodePackageNotFound, CodeAccountNotFound:
		return http.StatusNotFound
	case CodeConflict, CodeVersionExists:
		return http.StatusConflict
	case CodeTimeout:
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

// Is 判断错误类型
func Is(err error, code Code) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == code
	}
	return false
}
