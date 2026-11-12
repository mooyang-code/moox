// Package trpclog contains shared tRPC logging helpers.
package trpclog

import (
	"strings"

	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceNameField is the CLS field used to identify the emitting service.
const ServiceNameField = "service_name"

// InstallServiceName decorates the process-wide tRPC logger with a stable
// service_name field. It must be called after trpc.NewServer, because NewServer
// loads trpc_go.yaml and replaces the default logger during plugin setup.
func InstallServiceName(serviceName string) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return
	}

	base := log.GetDefaultLogger()
	if base == nil {
		return
	}
	if tagged, ok := base.(*serviceNameLogger); ok {
		if tagged.serviceName == serviceName {
			return
		}
		base = tagged.base
	}

	log.SetLogger(newServiceNameLogger(base, serviceName))
}

// serviceNameLogger 给进程的默认日志对象加上 service_name 字段。
//
// 日志里的代码位置靠“跳过几层调用”算出来，包装多了一层调用，所以要分两种用法：
//   - 直接调用包装对象的方法（log.Errorf 等最终走到这里）：比原来多一层，用 tagged，它多跳一层。
//   - 经 With 取出的对象（log.With、log.WithContextFields 之后的调用）：tRPC 直接调用取出的对象，没有多出来的那一层，
//     所以 With 必须从没有多跳的 plain 派生。
//
// 这两处原来搞反了（tagged 少跳一层、With 又从 tagged 派生），线上日志里的代码位置因此全都指向本文件。
type serviceNameLogger struct {
	base        log.Logger
	plain       log.Logger
	tagged      log.Logger
	serviceName string
}

func newServiceNameLogger(base log.Logger, serviceName string) *serviceNameLogger {
	field := log.Field{Key: ServiceNameField, Value: serviceName}
	wrapped := base
	if optionLogger, ok := base.(log.OptionLogger); ok {
		wrapped = optionLogger.WithOptions(log.WithAdditionalCallerSkip(1))
	}
	return &serviceNameLogger{
		base:        base,
		plain:       base.With(field),
		tagged:      wrapped.With(field),
		serviceName: serviceName,
	}
}

func (l *serviceNameLogger) Trace(args ...interface{}) { l.tagged.Trace(args...) }
func (l *serviceNameLogger) Tracef(format string, args ...interface{}) {
	l.tagged.Tracef(format, args...)
}
func (l *serviceNameLogger) Debug(args ...interface{}) { l.tagged.Debug(args...) }
func (l *serviceNameLogger) Debugf(format string, args ...interface{}) {
	l.tagged.Debugf(format, args...)
}
func (l *serviceNameLogger) Info(args ...interface{}) { l.tagged.Info(args...) }
func (l *serviceNameLogger) Infof(format string, args ...interface{}) {
	l.tagged.Infof(format, args...)
}
func (l *serviceNameLogger) Warn(args ...interface{}) { l.tagged.Warn(args...) }
func (l *serviceNameLogger) Warnf(format string, args ...interface{}) {
	l.tagged.Warnf(format, args...)
}
func (l *serviceNameLogger) Error(args ...interface{}) { l.tagged.Error(args...) }
func (l *serviceNameLogger) Errorf(format string, args ...interface{}) {
	l.tagged.Errorf(format, args...)
}
func (l *serviceNameLogger) Fatal(args ...interface{}) { l.tagged.Fatal(args...) }
func (l *serviceNameLogger) Fatalf(format string, args ...interface{}) {
	l.tagged.Fatalf(format, args...)
}
func (l *serviceNameLogger) Sync() error                             { return l.base.Sync() }
func (l *serviceNameLogger) SetLevel(output string, level log.Level) { l.base.SetLevel(output, level) }
func (l *serviceNameLogger) GetLevel(output string) log.Level        { return l.base.GetLevel(output) }

func (l *serviceNameLogger) With(fields ...log.Field) log.Logger {
	filtered := make([]log.Field, 0, len(fields))
	for _, field := range fields {
		if field.Key != ServiceNameField {
			filtered = append(filtered, field)
		}
	}
	return l.plain.With(filtered...)
}

// WithOptions preserves tRPC's caller-skip adjustment when log.With or
// log.WithContext is called against the decorated default logger.
func (l *serviceNameLogger) WithOptions(opts ...log.Option) log.Logger {
	optionLogger, ok := l.base.(log.OptionLogger)
	if !ok {
		return l
	}
	base := optionLogger.WithOptions(opts...)
	return newServiceNameLogger(base, l.serviceName)
}
