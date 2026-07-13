package trpclog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/log"
)

func TestInstallServiceNameAddsStableField(t *testing.T) {
	base := &captureLogger{}
	base.entries = &[]map[string]interface{}{}
	old := log.GetDefaultLogger()
	defer log.SetLogger(old)
	log.SetLogger(base)

	InstallServiceName("factor")
	log.Info("startup")
	log.With(log.Field{Key: "request_id", Value: "req-1"}, log.Field{Key: ServiceNameField, Value: "spoofed"}).Info("request")

	require.Len(t, *base.entries, 2)
	require.Equal(t, "factor", (*base.entries)[0][ServiceNameField])
	require.Equal(t, "factor", (*base.entries)[1][ServiceNameField])
	require.Equal(t, "req-1", (*base.entries)[1]["request_id"])
}

type captureLogger struct {
	fields  map[string]interface{}
	entries *[]map[string]interface{}
}

func (l *captureLogger) clone(fields ...log.Field) *captureLogger {
	entries := l.entries
	if entries == nil {
		entries = &[]map[string]interface{}{}
	}
	clone := &captureLogger{fields: make(map[string]interface{}, len(l.fields)), entries: entries}
	for key, value := range l.fields {
		clone.fields[key] = value
	}
	for _, field := range fields {
		clone.fields[field.Key] = field.Value
	}
	return clone
}

func (l *captureLogger) record() {
	entry := make(map[string]interface{}, len(l.fields))
	for key, value := range l.fields {
		entry[key] = value
	}
	*l.entries = append(*l.entries, entry)
}

func (l *captureLogger) Trace(...interface{})                { l.record() }
func (l *captureLogger) Tracef(string, ...interface{})       { l.record() }
func (l *captureLogger) Debug(...interface{})                { l.record() }
func (l *captureLogger) Debugf(string, ...interface{})       { l.record() }
func (l *captureLogger) Info(...interface{})                 { l.record() }
func (l *captureLogger) Infof(string, ...interface{})        { l.record() }
func (l *captureLogger) Warn(...interface{})                 { l.record() }
func (l *captureLogger) Warnf(string, ...interface{})        { l.record() }
func (l *captureLogger) Error(...interface{})                { l.record() }
func (l *captureLogger) Errorf(string, ...interface{})       { l.record() }
func (l *captureLogger) Fatal(...interface{})                { l.record() }
func (l *captureLogger) Fatalf(string, ...interface{})       { l.record() }
func (l *captureLogger) Sync() error                         { return nil }
func (l *captureLogger) SetLevel(string, log.Level)          {}
func (l *captureLogger) GetLevel(string) log.Level           { return log.LevelInfo }
func (l *captureLogger) With(fields ...log.Field) log.Logger { return l.clone(fields...) }
