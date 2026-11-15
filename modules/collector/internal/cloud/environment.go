package cloud

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

const (
	// packageIDKey 是函数环境里的代码包 ID。它兼作“代码已换好”的标记：只在换代码成功之后写入。
	packageIDKey = "MOOX_CODE_PACKAGE_ID"
	// planKey 是定时采集函数的采集计划，必须与 marketfetch.PlanEnvName 一致。
	planKey = "MOOX_MARKET_FETCH_PLAN"
	// modeKey 为 backfill 时是补采函数。
	modeKey = "MOOX_MARKET_FETCH_MODE"
	// timeoutKey 是函数内单次运行的超时，必须与函数配置的超时一致（60 秒）。
	timeoutKey = "MOOX_FETCH_TIMEOUT_SECONDS"
)

// removedEnvironmentKeys 是写入时从云上现有环境里删掉的键：创建预留号（预留机制已删除），
// 以及已经废弃的 DNS 受管键。写入以云上环境为底合并，不显式删掉的话会一直留在按函数名接管的旧函数上。
var removedEnvironmentKeys = []string{
	"MOOX_CREATE_RESERVATION_ID",
	"MOOX_MARKET_FETCH_DNS_ROUTES_JSON",
	"MOOX_MARKET_FETCH_DNS_HASH",
	"MOOX_MARKET_FETCH_DNS_UPDATED_AT",
}

// secretEnvironmentKeys 与 secretEnvironmentSuffixes 是读回结果带出包外之前去掉的密钥项：调用方密钥、Storage 应用密钥、
// CLS 写入密钥，以及名字按后缀看像密钥的项（接管的旧函数或基础环境文件里可能带着别的密钥）。
var (
	secretEnvironmentKeys     = []string{"MOOX_CALLER_KEY", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "MOOX_CLS_SECRET_KEY"}
	secretEnvironmentSuffixes = []string{"_KEY", "_SECRET", "_PASSWORD", "_TOKEN", "_CREDENTIALS"}
)

// mergeEnvironment 以云上现有环境为底，删掉废弃键，再覆盖期望值。
func mergeEnvironment(current, desired map[string]string) map[string]string {
	out := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		out[key] = value
	}
	for _, key := range removedEnvironmentKeys {
		delete(out, key)
	}
	for key, value := range desired {
		out[key] = value
	}
	return out
}

// environmentCovers 判断 want 的每一项在 current 里都有且值相等；current 多出来的项不算不一致。
func environmentCovers(current, want map[string]string) bool {
	for key, value := range want {
		if actual, ok := current[key]; !ok || actual != value {
			return false
		}
	}
	return true
}

// environmentMatches 判断云上环境已经是期望的样子：期望的每一项都在且相等，并且没有要删掉的废弃键（创建预留号、DNS 键）。
// 云上还带着废弃键时算不一致，让 Ensure 写一次把它们删掉。
// 只用于决定要不要写；写后读回校验用 environmentCovers，免得云上按键合并环境时废弃键删不掉而永远校验失败。
func environmentMatches(current, want map[string]string) bool {
	for _, key := range removedEnvironmentKeys {
		if _, ok := current[key]; ok {
			return false
		}
	}
	return environmentCovers(current, want)
}

// publicEnvironment 返回去掉密钥项的环境副本，供包外比对与展示。
func publicEnvironment(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		if !isSecretEnvironmentKey(key) {
			out[key] = value
		}
	}
	return out
}

func isSecretEnvironmentKey(key string) bool {
	if slices.Contains(secretEnvironmentKeys, key) {
		return true
	}
	upper := strings.ToUpper(key)
	for _, suffix := range secretEnvironmentSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

// validateEnvironment 校验写给函数的完整环境。cloud 包只管两种函数：带采集计划的定时采集函数与补采函数；
// 两者都不连 EventBus（环境里出现 EventBus 项直接拒绝），都经 Access 写 Storage、带 CLS 上报配置，单次运行超时都是 60 秒，
// 合计不超过 4096 字节。
func validateEnvironment(values map[string]string) error {
	if strings.TrimSpace(values[planKey]) == "" && strings.TrimSpace(values[modeKey]) != "backfill" {
		return fmt.Errorf("只管理带采集计划的定时采集函数与补采函数：环境里没有 %s，%s 也不是 backfill", planKey, modeKey)
	}
	if err := tencent.ValidateCollectorMarketFetchEnvironment(values); err != nil {
		return err
	}
	if values[timeoutKey] != strconv.Itoa(tencent.CollectorTimerTimeoutSeconds) {
		return fmt.Errorf("环境里的 %s 必须是 %d", timeoutKey, tencent.CollectorTimerTimeoutSeconds)
	}
	return nil
}

// isSupportedTimerCron 判断定时器的 cron 是不是采集计划会用到的那几种：秒位可错开，
// 每小时一次的分片还可以错开分钟位，其余只能是固定的几种周期。
func isSupportedTimerCron(cron string) bool {
	fields := strings.Fields(cron)
	if len(fields) != 7 {
		return false
	}
	second, err := strconv.Atoi(fields[0])
	if err != nil || second < 0 || second > 59 {
		return false
	}
	if fields[1] != "*" {
		minute, minuteErr := strconv.Atoi(fields[1])
		if minuteErr == nil && minute >= 0 && minute <= 59 && strings.Join(fields[2:], " ") == "* * * * *" {
			return true
		}
	}
	fields[0] = "0"
	switch strings.Join(fields, " ") {
	case "0 * * * * * *", "0 */5 * * * * *", "0 */15 * * * * *", "0 */30 * * * * *", "0 0 * * * * *", "0 0 */4 * * * *", "0 0 0 * * * *":
		return true
	default:
		return false
	}
}
