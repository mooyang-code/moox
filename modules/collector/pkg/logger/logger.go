package logger

import (
	"context"
	"log/slog"
	"os"
)

// Logger 结构化日志接口
type Logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
	With(args ...interface{}) Logger
}

// contextKey 是用于在 context 中存储 Logger 的 key
type contextKey struct{}

// WithContext 将 Logger 存储到 context 中
func WithContext(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext 从 context 中获取 Logger
func FromContext(ctx context.Context) (Logger, bool) {
	logger, ok := ctx.Value(contextKey{}).(Logger)
	return logger, ok
}

// InfoContextf 从 context 中获取 Logger 并输出 info 日志，自动添加前缀
func InfoContextf(ctx context.Context, msg string, args ...interface{}) {
	if logger, ok := FromContext(ctx); ok {
		logger.Info(msg, args...)
	}
}

// DebugContextf 从 context 中获取 Logger 并输出 debug 日志，自动添加前缀
func DebugContextf(ctx context.Context, msg string, args ...interface{}) {
	if logger, ok := FromContext(ctx); ok {
		logger.Debug(msg, args...)
	}
}

// WarnContextf 从 context 中获取 Logger 并输出 warn 日志，自动添加前缀
func WarnContextf(ctx context.Context, msg string, args ...interface{}) {
	if logger, ok := FromContext(ctx); ok {
		logger.Warn(msg, args...)
	}
}

// ErrorContextf 从 context 中获取 Logger 并输出 error 日志，自动添加前缀
func ErrorContextf(ctx context.Context, msg string, args ...interface{}) {
	if logger, ok := FromContext(ctx); ok {
		logger.Error(msg, args...)
	}
}

// Config 日志配置
type Config struct {
	Level  string `json:"level" yaml:"level"`
	Format string `json:"format" yaml:"format"` // json, text
	NodeID string `json:"node_id" yaml:"node_id"`
}

// structuredLogger 结构化日志实现
type structuredLogger struct {
	logger *slog.Logger
	nodeID string
}

// New 创建新的日志实例
func New(cfg Config) Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler).With(
		"node_id", cfg.NodeID,
		"component", "data-collector",
	)

	return &structuredLogger{
		logger: logger,
		nodeID: cfg.NodeID,
	}
}

// NewDefault 创建默认日志实例
func NewDefault() Logger {
	return New(Config{
		Level:  "info",
		Format: "json",
		NodeID: "unknown",
	})
}

func (l *structuredLogger) Debug(msg string, args ...interface{}) {
	l.logger.Debug("[***DATA-COLLECTOR***] "+msg, l.parseArgs(args...)...)
}

func (l *structuredLogger) Info(msg string, args ...interface{}) {
	l.logger.Info("[***DATA-COLLECTOR***] "+msg, l.parseArgs(args...)...)
}

func (l *structuredLogger) Warn(msg string, args ...interface{}) {
	l.logger.Warn("[***DATA-COLLECTOR***] "+msg, l.parseArgs(args...)...)
}

func (l *structuredLogger) Error(msg string, args ...interface{}) {
	l.logger.Error("[***DATA-COLLECTOR***] "+msg, l.parseArgs(args...)...)
}

func (l *structuredLogger) With(args ...interface{}) Logger {
	return &structuredLogger{
		logger: l.logger.With(l.parseArgs(args...)...),
		nodeID: l.nodeID,
	}
}

// parseArgs 解析日志参数，支持键值对和单个值
func (l *structuredLogger) parseArgs(args ...interface{}) []interface{} {
	result := make([]interface{}, 0, len(args))

	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			// 键值对
			result = append(result, args[i], args[i+1])
		} else {
			// 单个值，使用默认键
			result = append(result, "value", args[i])
		}
	}

	return result
}

// With 创建带上下文的日志实例
func With(args ...interface{}) Logger {
	globalLogger := NewDefault()
	return globalLogger.With(args...)
}

// Global 全局日志实例
var Global Logger = NewDefault()

// SetGlobal 设置全局日志实例
func SetGlobal(l Logger) {
	Global = l
}

// Debug 输出调试信息
func Debug(msg string, args ...interface{}) {
	Global.Debug(msg, args...)
}

// Info 输出普通信息
func Info(msg string, args ...interface{}) {
	Global.Info(msg, args...)
}

// Warn 输出警告信息
func Warn(msg string, args ...interface{}) {
	Global.Warn(msg, args...)
}

// Error 输出错误信息
func Error(msg string, args ...interface{}) {
	Global.Error(msg, args...)
}
