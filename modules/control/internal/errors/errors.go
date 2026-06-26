// Package errors 提供统一的错误处理机制
package errors

import (
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
	CodeTaskCancelled     Code = "TASK_CANCELLED"
	CodePackageNotFound   Code = "PACKAGE_NOT_FOUND"
	CodeAccountNotFound   Code = "ACCOUNT_NOT_FOUND"
	CodeInvalidConfig     Code = "INVALID_CONFIG"
	CodeProviderNotFound  Code = "PROVIDER_NOT_FOUND"
	CodeInvalidFormat     Code = "INVALID_FORMAT"
	CodeMethodNotAllowed  Code = "METHOD_NOT_ALLOWED"
	CodeFileNotFound      Code = "FILE_NOT_FOUND"
	CodeUploadFailed      Code = "UPLOAD_FAILED"
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

// Internal 内部错误
func Internal(message string, err error) *AppError {
	return &AppError{
		Code:       CodeInternal,
		Message:    message,
		HTTPStatus: http.StatusInternalServerError,
		Err:        err,
	}
}

// Conflict 资源状态冲突
func Conflict(message string, err error) *AppError {
	return &AppError{
		Code:       CodeConflict,
		Message:    message,
		HTTPStatus: http.StatusConflict,
		Err:        err,
	}
}
