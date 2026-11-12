// Package logcls 让一个服务接入集中的日志：服务的 main 空白引入本包即可。
//
//   - 重要日志（Error 及以上、关键事件）交给 packages/report/logsink，随后由 report.NewHandler 接到 EventBus 发给 Monitor。
//     这一步不需要任何配置。
//   - 运行环境里启用了 CLS 时，警告及以上的日志和关键事件另外上报到腾讯云日志服务；没有启用时这一路不存在。
//
// CLS 的环境变量是 MOOX_LOG_CLS_ENABLED、_ENDPOINT、_TOPIC_ID、_SECRET_ID、_SECRET_KEY、_SOURCE、_TIMEOUT_MS，含义与云函数的
// MOOX_CLS_* 相同（见 clsreporter.ConfigFromEnv），只是前缀不同：云函数和 CloudFunction 的那一组指向采集日志的主题和另一把密钥，
// 两组不能混用。另有 MOOX_LOG_CLS_MIN_LEVEL（默认 warn）控制上报的最低级别；关键事件不论级别都上报。
//
// 上报走明文 HTTP（SDK 写死），所以日志里不要写密钥和完整的 Webhook 地址。
package logcls

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"go.uber.org/zap/zapcore"
)

func init() {
	// 接管默认日志的装配，给它加上 moox 输出：重要日志从此发给 Monitor，不需要改 trpc_go.yaml。
	logsink.InstallDefaultFactory()
	logsink.OnSetup(func() { setup(os.Getenv) })
}

func setup(getenv func(string) string) {
	cfg, enabled, err := clsreporter.ConfigFromEnv(serviceLogEnv(getenv))
	if err != nil {
		// 这里不能用 tRPC 日志（日志插件正在装配）；写到标准错误，由主机守护的输出文件留存。
		fmt.Fprintf(os.Stderr, "logcls: CLS 日志上报没有启用：%v\n", err)
		return
	}
	if !enabled {
		return
	}
	level, err := parseLevel(getenv("MOOX_LOG_CLS_MIN_LEVEL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcls: CLS 日志上报没有启用：%v\n", err)
		return
	}
	reporter, err := clsreporter.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcls: CLS 日志上报没有启用：%v\n", err)
		return
	}
	identity := logsink.Identity{
		ServiceName: strings.TrimSpace(getenv("MOOX_SERVICE_NAME")), InstanceID: strings.TrimSpace(getenv("MOOX_INSTANCE_ID")),
		NodeID: strings.TrimSpace(getenv("MOOX_NODE_ID")), BootID: strings.TrimSpace(getenv("MOOX_BOOT_ID")),
	}
	logsink.SetShipper(identity, level, func(ctx context.Context, identity logsink.Identity, records []logsink.Record) error {
		for _, record := range records {
			reporter.Report(clsreporter.Entry{Timestamp: record.Time, Fields: Fields(identity, record)})
		}
		return reporter.Flush(ctx)
	})
}

// serviceLogEnv 把 clsreporter 按 MOOX_CLS_* 取的值换成服务日志自己的前缀 MOOX_LOG_CLS_*。
// 两组不能混：MOOX_CLS_* 是云函数采集日志用的，指向另一个主题和另一把密钥，CloudFunction 进程的环境里两组可能同时存在。
func serviceLogEnv(getenv func(string) string) func(string) string {
	return func(key string) string {
		return getenv(strings.Replace(key, "MOOX_CLS_", "MOOX_LOG_CLS_", 1))
	}
}

func parseLevel(value string) (zapcore.Level, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	// 默认只上报警告及以上（关键事件另算，不受这个级别限制）：Info 级的逐条处理日志量大、用处小，留在本机文件里。
	if value == "" {
		return zapcore.WarnLevel, nil
	}
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return 0, fmt.Errorf("MOOX_LOG_CLS_MIN_LEVEL 只能是 debug、info、warn、error：%q", value)
	}
	return level, nil
}

// reserved 是每条日志固定带的字段名；日志自带的字段和它们重名时加上 f_ 前缀，不覆盖固定字段。
var reserved = map[string]struct{}{
	"level": {}, "message": {}, "caller": {}, "event": {}, "component": {}, "host": {}, "instance": {}, "boot_id": {}, "merged_count": {},
}

// Fields 把一条日志整理成 CLS 的键值字段。
func Fields(identity logsink.Identity, record logsink.Record) map[string]string {
	fields := make(map[string]string, len(record.Fields)+8)
	for key, value := range record.Fields {
		if _, taken := reserved[key]; taken {
			key = "f_" + key
		}
		fields[key] = value
	}
	fields["level"], fields["message"] = record.Level, record.Message
	fields["component"], fields["host"], fields["instance"], fields["boot_id"] = identity.ServiceName, identity.NodeID, identity.InstanceID, identity.BootID
	if record.Caller != "" {
		fields["caller"] = record.Caller
	}
	if record.Event != "" {
		fields["event"] = record.Event
	}
	if record.MergedCount > 0 {
		fields["merged_count"] = strconv.FormatUint(uint64(record.MergedCount), 10)
	}
	return fields
}
