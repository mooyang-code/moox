package tencent

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const SCFEnvironmentLimitBytes = 4096
const CollectorTimerTimeoutSeconds = 60

// SCF 采集函数经外部接入访问 MooX 使用的环境变量，与 gatewayclient 外部方式读取的变量一致。
const (
	EnvAccessAddress = "MOOX_ACCESS_ADDRESS"
	EnvAccessID      = "MOOX_ACCESS_ID"
	EnvCaller        = "MOOX_CALLER"
	EnvCallerKey     = "MOOX_CALLER_KEY"
	// SCFCollectorCaller 是 SCF 采集函数的外部调用方身份。
	SCFCollectorCaller = "scf-collector"
)

var accessHostID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// ValidateCollectorMarketFetchEnvironment 检查采集函数合并后的完整环境变量，Timer 和 Invoke 共用；
// CLI 发布与 CloudNode 部署都调用它，部分更新也绕不过创建时的检查。
func ValidateCollectorMarketFetchEnvironment(values map[string]string) error {
	if err := ValidateSCFEnvironment(values); err != nil {
		return err
	}
	for _, key := range []string{
		"MOOX_SPACE_ID", "MOOX_CODE_PACKAGE_ID",
		EnvCaller, EnvCallerKey, EnvAccessAddress, EnvAccessID,
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON",
		"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_TIMEOUT_MS",
		"MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY",
		"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE",
	} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("采集函数的环境变量缺少 %s", key)
		}
	}
	if err := validateStorageAppKeys(values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"]); err != nil {
		return fmt.Errorf("采集函数的 MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON 无效: %w", err)
	}
	if strings.TrimSpace(values[EnvCaller]) != SCFCollectorCaller {
		return fmt.Errorf("采集函数的 %s 必须是 %s", EnvCaller, SCFCollectorCaller)
	}
	if keyID, secret, ok := strings.Cut(strings.TrimSpace(values[EnvCallerKey]), ":"); !ok || strings.TrimSpace(keyID) == "" || strings.TrimSpace(secret) == "" {
		return fmt.Errorf("采集函数的 %s 格式应为 <key_id>:<secret>", EnvCallerKey)
	}
	if err := validateAccessAddress(values[EnvAccessAddress]); err != nil {
		return err
	}
	if host, ok := strings.CutPrefix(strings.TrimSpace(values[EnvAccessID]), "access@"); !ok || !accessHostID.MatchString(host) {
		return fmt.Errorf("采集函数的 %s 必须是 access@<主机 ID>", EnvAccessID)
	}
	if err := validateEventBusURL(values["MOOX_EVENTBUS_NATS_URL"]); err != nil {
		return err
	}
	if enabled, err := strconv.ParseBool(values["MOOX_CLS_ENABLED"]); err != nil || !enabled {
		return fmt.Errorf("采集函数的环境变量要求 MOOX_CLS_ENABLED=true")
	}
	if timeout, err := strconv.Atoi(values["MOOX_CLS_TIMEOUT_MS"]); err != nil || timeout <= 0 {
		return fmt.Errorf("采集函数的 MOOX_CLS_TIMEOUT_MS 无效")
	}
	if values["MOOX_EVENTBUS_NATS_TLS_CA_FILE"] != "certs/eventbus-ca.pem" {
		return fmt.Errorf("采集函数的 MOOX_EVENTBUS_NATS_TLS_CA_FILE 必须指向代码包内的 certs/eventbus-ca.pem")
	}
	if strings.TrimSpace(values["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"]) != "" {
		return fmt.Errorf("采集函数不能同时配置 CA 文件和 MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	}
	return nil
}

// validateAccessAddress 检查外部接入地址：host:port，不能是本机地址。
func validateAccessAddress(value string) error {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || host == "" {
		return fmt.Errorf("采集函数的 %s 必须是 host:port", EnvAccessAddress)
	}
	if port, err := strconv.Atoi(rawPort); err != nil || port < 1 || port > 65535 || isLocalHost(host) {
		return fmt.Errorf("采集函数的 %s 无效", EnvAccessAddress)
	}
	return nil
}

func validateEventBusURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "tls" || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("采集函数的 MOOX_EVENTBUS_NATS_URL 必须是 tls://host:port")
	}
	if port, err := strconv.Atoi(parsed.Port()); err != nil || port < 1 || port > 65535 || isLocalHost(parsed.Hostname()) {
		return fmt.Errorf("采集函数的 MOOX_EVENTBUS_NATS_URL 无效")
	}
	return nil
}

func isLocalHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || strings.EqualFold(host, "ip6-localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified()))
}

func validateStorageAppKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("必须是 JSON 对象")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		appID, ok := token.(string)
		if err != nil || !ok || strings.TrimSpace(appID) == "" {
			return fmt.Errorf("app ID 无效")
		}
		if _, duplicate := seen[appID]; duplicate {
			return fmt.Errorf("app ID 重复")
		}
		seen[appID] = struct{}{}
		var key string
		if err := decoder.Decode(&key); err != nil || len(key) != 64 {
			return fmt.Errorf("app key 必须是 64 位十六进制字符")
		}
		if _, err := hex.DecodeString(key); err != nil {
			return fmt.Errorf("app key 必须是 64 位十六进制字符")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("app key 对象无效")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON 之后有多余内容")
	}
	if len(seen) == 0 {
		return fmt.Errorf("app key 不能为空")
	}
	if _, ok := seen["moox-collector"]; !ok {
		return fmt.Errorf("app key 必须包含 moox-collector 的绑定")
	}
	return nil
}

func SCFEnvironmentBytes(values map[string]string) int {
	total := 0
	for key, value := range values {
		total += len(key) + 1 + len(value) + 1
	}
	return total
}

// ValidateSCFEnvironment 检查合并后的完整环境变量；报错时只给出各变量的长度，不暴露凭据。
func ValidateSCFEnvironment(values map[string]string) error {
	if value, exists := values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"]; exists {
		return fmt.Errorf("SCF 环境变量不能包含 MOOX_STORAGE_PRIMARY_AUTH_SECRET（值长度 %d）", len(value))
	}
	size := SCFEnvironmentBytes(values)
	if size <= SCFEnvironmentLimitBytes {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lengths := make([]string, 0, len(keys))
	for _, key := range keys {
		lengths = append(lengths, fmt.Sprintf("%s:%d", key, len(values[key])))
	}
	return fmt.Errorf("环境变量共 %d 字节，超过上限 %d（各值长度：%s）", size, SCFEnvironmentLimitBytes, strings.Join(lengths, ", "))
}
