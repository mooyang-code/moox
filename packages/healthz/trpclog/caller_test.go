package trpclog

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/plugin"
)

// callerCore 记下每条日志的代码位置和字段，用真实的 tRPC 日志对象验证包装之后位置没有算错。
type callerCore struct {
	mu      *sync.Mutex
	entries *[]capturedEntry
	fields  []zapcore.Field
}

type capturedEntry struct {
	caller string
	fields map[string]interface{}
}

func (c callerCore) Enabled(zapcore.Level) bool { return true }
func (c callerCore) With(fields []zapcore.Field) zapcore.Core {
	c.fields = append(append([]zapcore.Field(nil), c.fields...), fields...)
	return c
}
func (c callerCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return checked.AddCore(entry, c)
}
func (c callerCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range append(append([]zapcore.Field(nil), c.fields...), fields...) {
		field.AddTo(encoder)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.entries = append(*c.entries, capturedEntry{
		caller: fmt.Sprintf("%s:%d", filepath.Base(entry.Caller.File), entry.Caller.Line), fields: encoder.Fields,
	})
	return nil
}
func (c callerCore) Sync() error { return nil }

type callerWriter struct{ core callerCore }

func (callerWriter) Type() string { return "log" }
func (w callerWriter) Setup(_ string, decoder plugin.Decoder) error {
	output := decoder.(*log.Decoder)
	output.Core, output.ZapLevel = w.core, zap.NewAtomicLevelAt(zap.DebugLevel)
	return nil
}

// here 返回调用处的“文件名:行号”，offset 是相对调用行的偏移。
func here(offset int) string {
	_, file, line, _ := runtime.Caller(1)
	return fmt.Sprintf("%s:%d", filepath.Base(file), line+offset)
}

// 线上用到的四种写法，记下的代码位置都必须是调用处，而且都带 service_name。
func TestInstallServiceNameKeepsCallerLocation(t *testing.T) {
	var mu sync.Mutex
	var entries []capturedEntry
	log.RegisterWriter("trpclog-caller-test", callerWriter{core: callerCore{mu: &mu, entries: &entries}})
	old := log.GetDefaultLogger()
	defer log.SetLogger(old)
	log.SetLogger(log.NewZapLog(log.Config{{Writer: "trpclog-caller-test", Level: "debug"}}))
	InstallServiceName("monitor")

	var want []string
	want = append(want, here(1))
	log.Errorf("直接调用 %d", 1)

	ctx := trpc.BackgroundContext()
	want = append(want, here(1))
	log.ErrorContextf(ctx, "带上下文、没有字段 %d", 2)

	log.WithContextFields(ctx, "request_id", "req-3")
	want = append(want, here(1))
	log.ErrorContextf(ctx, "带上下文字段 %d", 3)

	want = append(want, here(1))
	log.With(log.Field{Key: "task", Value: "t-4"}).Errorf("With 之后调用 %d", 4)

	want = append(want, here(1))
	log.With(log.Field{Key: "task", Value: "t-5"}).With(log.Field{Key: "step", Value: "s"}).Warn("连续 With")

	if len(entries) != len(want) {
		t.Fatalf("记录了 %d 条日志，应为 %d 条", len(entries), len(want))
	}
	for i, entry := range entries {
		if entry.caller != want[i] {
			t.Errorf("第 %d 种写法的代码位置 = %s，应为 %s", i+1, entry.caller, want[i])
		}
		if entry.fields[ServiceNameField] != "monitor" {
			t.Errorf("第 %d 种写法缺少 service_name：%v", i+1, entry.fields)
		}
	}
	if entries[2].fields["request_id"] != "req-3" || entries[3].fields["task"] != "t-4" || entries[4].fields["step"] != "s" {
		t.Fatalf("字段丢失：%v", entries)
	}
}
