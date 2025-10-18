// Package logger 提供统一的日志接口，基于trpc-go/log
package logger

import (
	"context"
	"fmt"

	trpclog "trpc.group/trpc-go/trpc-go/log"
)

// Logger 日志接口
type Logger interface {
	// Debug 调试日志
	Debug(ctx context.Context, msg string, fields ...Field)
	// Info 信息日志
	Info(ctx context.Context, msg string, fields ...Field)
	// Warn 警告日志
	Warn(ctx context.Context, msg string, fields ...Field)
	// Error 错误日志
	Error(ctx context.Context, msg string, fields ...Field)
}

// Field 日志字段
type Field struct {
	Key   string
	Value interface{}
}

// 字段构造函数

// String 字符串字段
func String(key, value string) Field {
	return Field{Key: key, Value: value}
}

// Int 整数字段
func Int(key string, value int) Field {
	return Field{Key: key, Value: value}
}

// Int64 64位整数字段
func Int64(key string, value int64) Field {
	return Field{Key: key, Value: value}
}

// Bool 布尔字段
func Bool(key string, value bool) Field {
	return Field{Key: key, Value: value}
}

// Err 错误字段
func Err(err error) Field {
	return Field{Key: "error", Value: err}
}

// Any 任意类型字段
func Any(key string, value interface{}) Field {
	return Field{Key: key, Value: value}
}

// TrpcLogger 基于trpc-go/log的实现
type TrpcLogger struct{}

// NewTrpcLogger 创建基于trpc的日志器
func NewTrpcLogger() Logger {
	return &TrpcLogger{}
}

// Debug 调试日志
func (l *TrpcLogger) Debug(ctx context.Context, msg string, fields ...Field) {
	if len(fields) == 0 {
		trpclog.DebugContext(ctx, msg)
		return
	}
	trpclog.DebugContextf(ctx, "%s %s", msg, formatFields(fields))
}

// Info 信息日志
func (l *TrpcLogger) Info(ctx context.Context, msg string, fields ...Field) {
	if len(fields) == 0 {
		trpclog.InfoContext(ctx, msg)
		return
	}
	trpclog.InfoContextf(ctx, "%s %s", msg, formatFields(fields))
}

// Warn 警告日志
func (l *TrpcLogger) Warn(ctx context.Context, msg string, fields ...Field) {
	if len(fields) == 0 {
		trpclog.WarnContext(ctx, msg)
		return
	}
	trpclog.WarnContextf(ctx, "%s %s", msg, formatFields(fields))
}

// Error 错误日志
func (l *TrpcLogger) Error(ctx context.Context, msg string, fields ...Field) {
	if len(fields) == 0 {
		trpclog.ErrorContext(ctx, msg)
		return
	}
	trpclog.ErrorContextf(ctx, "%s %s", msg, formatFields(fields))
}

// formatFields 格式化字段
func formatFields(fields []Field) string {
	if len(fields) == 0 {
		return ""
	}

	result := "["
	for i, field := range fields {
		if i > 0 {
			result += ", "
		}
		result += fmt.Sprintf("%s=%v", field.Key, field.Value)
	}
	result += "]"
	return result
}

// 全局默认日志器
var defaultLogger Logger = NewTrpcLogger()

// SetDefault 设置默认日志器
func SetDefault(l Logger) {
	defaultLogger = l
}

// 便捷函数（使用默认日志器）

// Debug 调试日志
func Debug(ctx context.Context, msg string, fields ...Field) {
	defaultLogger.Debug(ctx, msg, fields...)
}

// Info 信息日志
func Info(ctx context.Context, msg string, fields ...Field) {
	defaultLogger.Info(ctx, msg, fields...)
}

// Warn 警告日志
func Warn(ctx context.Context, msg string, fields ...Field) {
	defaultLogger.Warn(ctx, msg, fields...)
}

// Error 错误日志
func Error(ctx context.Context, msg string, fields ...Field) {
	defaultLogger.Error(ctx, msg, fields...)
}

// Debugf 格式化调试日志
func Debugf(ctx context.Context, format string, args ...interface{}) {
	trpclog.DebugContextf(ctx, format, args...)
}

// Infof 格式化信息日志
func Infof(ctx context.Context, format string, args ...interface{}) {
	trpclog.InfoContextf(ctx, format, args...)
}

// Warnf 格式化警告日志
func Warnf(ctx context.Context, format string, args ...interface{}) {
	trpclog.WarnContextf(ctx, format, args...)
}

// Errorf 格式化错误日志
func Errorf(ctx context.Context, format string, args ...interface{}) {
	trpclog.ErrorContextf(ctx, format, args...)
}
