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

	log.SetLogger(newServiceNameLogger(base, serviceName, -1))
}

type serviceNameLogger struct {
	base        log.Logger
	tagged      log.Logger
	serviceName string
}

func newServiceNameLogger(base log.Logger, serviceName string, additionalCallerSkip int) *serviceNameLogger {
	taggedBase := base
	if additionalCallerSkip != 0 {
		if optionLogger, ok := base.(log.OptionLogger); ok {
			taggedBase = optionLogger.WithOptions(log.WithAdditionalCallerSkip(additionalCallerSkip))
		}
	}
	return &serviceNameLogger{
		base:        base,
		tagged:      taggedBase.With(log.Field{Key: ServiceNameField, Value: serviceName}),
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
	return l.tagged.With(filtered...)
}

// WithOptions preserves tRPC's caller-skip adjustment when log.With or
// log.WithContext is called against the decorated default logger.
func (l *serviceNameLogger) WithOptions(opts ...log.Option) log.Logger {
	optionLogger, ok := l.base.(log.OptionLogger)
	if !ok {
		return l
	}
	base := optionLogger.WithOptions(opts...)
	return newServiceNameLogger(base, l.serviceName, 0)
}
