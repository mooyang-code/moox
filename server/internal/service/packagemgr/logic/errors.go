package logic

import (
	"errors"
	"fmt"
)

// 定义业务错误类型
var (
	ErrValidationFailed = errors.New("validation failed")
	ErrVersionExists    = errors.New("version exists")
	ErrResourceNotFound = errors.New("resource not found")
)

// BusinessError 业务错误类型
type BusinessError struct {
	Type    error
	Message string
}

func (e *BusinessError) Error() string {
	return e.Message
}

func (e *BusinessError) Is(target error) bool {
	return e.Type == target
}

// NewValidationError 创建验证错误
func NewValidationError(message string) *BusinessError {
	return &BusinessError{Type: ErrValidationFailed, Message: message}
}

// NewVersionExistsError 创建版本已存在错误
func NewVersionExistsError(packageName, version string) *BusinessError {
	return &BusinessError{
		Type:    ErrVersionExists,
		Message: fmt.Sprintf("代码包 %s 版本 %s 已存在", packageName, version),
	}
}

// NewResourceNotFoundError 创建资源未找到错误
func NewResourceNotFoundError(message string) *BusinessError {
	return &BusinessError{Type: ErrResourceNotFound, Message: message}
}
