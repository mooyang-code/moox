package logcls

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"go.uber.org/zap/zapcore"
	"trpc.group/trpc-go/trpc-go/plugin"
)

func TestFieldsKeepFixedKeysAndPrefixCollisions(t *testing.T) {
	identity := logsink.Identity{ServiceName: "trade", InstanceID: "trade@overseas", NodeID: "overseas", BootID: "b1"}
	record := logsink.Record{
		Time: time.Unix(1700000000, 0), Level: "error", Message: "下单失败", Caller: "order/submit.go:88", Event: "order_rejected", MergedCount: 3,
		Fields: map[string]string{"account": "a1", "level": "自带的 level", "service_name": "trade"},
	}
	got := Fields(identity, record)
	want := map[string]string{
		"level": "error", "message": "下单失败", "caller": "order/submit.go:88", "event": "order_rejected", "merged_count": "3",
		"component": "trade", "host": "overseas", "instance": "trade@overseas", "boot_id": "b1",
		"account": "a1", "f_level": "自带的 level", "service_name": "trade",
	}
	if len(got) != len(want) {
		t.Fatalf("fields = %v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
	plain := Fields(identity, logsink.Record{Level: "info", Message: "m"})
	for _, absent := range []string{"caller", "event", "merged_count"} {
		if _, ok := plain[absent]; ok {
			t.Fatalf("空值不应出现：%s", absent)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for value, want := range map[string]zapcore.Level{"": zapcore.WarnLevel, "info": zapcore.InfoLevel, " WARN ": zapcore.WarnLevel, "error": zapcore.ErrorLevel, "debug": zapcore.DebugLevel} {
		got, err := parseLevel(value)
		if err != nil || got != want {
			t.Fatalf("parseLevel(%q) = %v, %v", value, got, err)
		}
	}
	if _, err := parseLevel("verbose"); err == nil {
		t.Fatal("不认识的级别应报错")
	}
}

// 没有启用、或者配置不完整时不接入，也不报错退出。
func TestSetupWithoutCLSDoesNothing(t *testing.T) {
	setup(func(string) string { return "" })
	setup(func(key string) string {
		if key == "MOOX_LOG_CLS_ENABLED" {
			return "true"
		}
		return ""
	})
}

// 服务日志只认 MOOX_LOG_CLS_*：环境里同时有云函数那一组 MOOX_CLS_* 时不能拿错。
func TestServiceLogEnvIgnoresFunctionVariables(t *testing.T) {
	env := map[string]string{
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "function.example", "MOOX_CLS_TOPIC_ID": "function-topic",
		"MOOX_CLS_SECRET_ID": "function-id", "MOOX_CLS_SECRET_KEY": "function-key",
	}
	getenv := func(key string) string { return env[key] }
	if _, enabled, err := clsreporter.ConfigFromEnv(serviceLogEnv(getenv)); err != nil || enabled {
		t.Fatalf("只有云函数那一组变量时不应启用：enabled=%v err=%v", enabled, err)
	}
	for key, value := range map[string]string{
		"MOOX_LOG_CLS_ENABLED": "true", "MOOX_LOG_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_LOG_CLS_TOPIC_ID": "service-topic",
		"MOOX_LOG_CLS_SECRET_ID": "writer-id", "MOOX_LOG_CLS_SECRET_KEY": "writer-key", "MOOX_LOG_CLS_SOURCE": "overseas",
	} {
		env[key] = value
	}
	cfg, enabled, err := clsreporter.ConfigFromEnv(serviceLogEnv(getenv))
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if cfg.Endpoint != "ap-guangzhou.cls.tencentcs.com" || cfg.TopicID != "service-topic" || cfg.SecretID != "writer-id" || cfg.SecretKey != "writer-key" || cfg.Source != "overseas" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// 引入本包就要接管默认日志的装配：去掉 init 里的 InstallDefaultFactory，所有服务的重要日志会悄悄消失。
func TestImportTakesOverDefaultLogFactory(t *testing.T) {
	factory := plugin.Get("log", "default")
	if got := fmt.Sprintf("%T", factory); !strings.HasSuffix(got, "logsink.defaultFactory") {
		t.Fatalf("默认日志的装配器是 %s，应当是 logsink 的", got)
	}
}
