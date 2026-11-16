package tencent

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const SCFEnvironmentLimitBytes = 4096
const CollectorTimerTimeoutSeconds = 60

var entryHostName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// ValidateCollectorTimerEnvironment 校验定时采集函数的环境：建函数与改环境（以云上环境为底合并后）用同一份规则，
// 改环境不能绕过建函数时的校验。
func ValidateCollectorTimerEnvironment(values map[string]string) error {
	return ValidateCollectorMarketFetchEnvironment(values)
}

// ValidateCollectorEntryEnvironment 校验函数经外部入口访问系统用的五个变量，报错里不带私钥内容。
// MOOX_CALLER_KEY 是 scf-collector 的 ed25519 私钥（base64 的 32 字节种子）；MOOX_CALLER_KEY_ID 是它的公钥指纹，
// 只用来判断云上函数的环境是不是还是当前这把密钥，不参与签名。
func ValidateCollectorEntryEnvironment(values map[string]string) error {
	for _, key := range []string{"MOOX_ENTRY_ADDRESS", "MOOX_ENTRY_HOST", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector entry environment requires %s", key)
		}
	}
	if values["MOOX_CALLER"] != "scf-collector" {
		return fmt.Errorf("collector entry environment requires MOOX_CALLER=scf-collector")
	}
	if len(values["MOOX_ENTRY_HOST"]) > 128 || !entryHostName.MatchString(values["MOOX_ENTRY_HOST"]) {
		return fmt.Errorf("collector entry environment has invalid MOOX_ENTRY_HOST")
	}
	if err := validateCollectorRuntimeEndpoint("MOOX_ENTRY_ADDRESS", "ip://"+values["MOOX_ENTRY_ADDRESS"], "ip"); err != nil {
		return err
	}
	keyID := values["MOOX_CALLER_KEY_ID"]
	if len(keyID) > 128 || strings.ContainsAny(keyID, "/\\") || strings.ContainsFunc(keyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("collector entry environment has invalid MOOX_CALLER_KEY_ID")
	}
	if seed, err := base64.StdEncoding.DecodeString(values["MOOX_CALLER_KEY"]); err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("collector entry environment has invalid MOOX_CALLER_KEY")
	}
	return nil
}

// collectorPlanEnvironmentKey 是定时触发采集计划的环境变量名，必须与 marketfetch.PlanEnvName 一致。
const collectorPlanEnvironmentKey = "MOOX_MARKET_FETCH_PLAN"

// ValidateCollectorMarketFetchEnvironment checks common Invoke and Timer
// dependencies. Claim and Storage use the same 外部入口 identity and connection.
func ValidateCollectorMarketFetchEnvironment(values map[string]string) error {
	// 采集计划函数与补采函数（MOOX_MARKET_FETCH_MODE=backfill，不带计划）都不连 EventBus。
	if strings.TrimSpace(values[collectorPlanEnvironmentKey]) != "" || strings.TrimSpace(values["MOOX_MARKET_FETCH_MODE"]) == "backfill" {
		return validateCollectorPlanEnvironment(values)
	}
	if err := ValidateSCFEnvironment(values); err != nil {
		return err
	}
	for _, key := range []string{
		"MOOX_SPACE_ID", "MOOX_CODE_PACKAGE_ID",
		"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_TIMEOUT_MS",
		"MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY",
		"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE",
	} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector market-fetch runtime environment requires %s", key)
		}
	}
	if err := ValidateCollectorEntryEnvironment(values); err != nil {
		return err
	}
	if err := validateCollectorRuntimeEndpoint("MOOX_EVENTBUS_NATS_URL", values["MOOX_EVENTBUS_NATS_URL"], "tls"); err != nil {
		return err
	}
	if enabled, err := strconv.ParseBool(values["MOOX_CLS_ENABLED"]); err != nil || !enabled {
		return fmt.Errorf("collector market-fetch runtime environment requires MOOX_CLS_ENABLED=true")
	}
	if timeout, err := strconv.Atoi(values["MOOX_CLS_TIMEOUT_MS"]); err != nil || timeout <= 0 {
		return fmt.Errorf("collector market-fetch runtime environment has invalid MOOX_CLS_TIMEOUT_MS")
	}
	if values["MOOX_EVENTBUS_NATS_TLS_CA_FILE"] != "certs/eventbus-ca.pem" {
		return fmt.Errorf("collector market-fetch runtime environment requires packaged MOOX_EVENTBUS_NATS_TLS_CA_FILE")
	}
	if strings.TrimSpace(values["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"]) != "" {
		return fmt.Errorf("collector market-fetch runtime environment cannot combine CA file with MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	}
	return nil
}

func validateCollectorRuntimeEndpoint(key, value, scheme string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != scheme || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("collector runtime environment requires %s as %s://host:port", key, scheme)
	}
	port, err := strconv.Atoi(parsed.Port())
	ip := net.ParseIP(parsed.Hostname())
	if err != nil || port < 1 || port > 65535 || strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "localhost") || strings.EqualFold(parsed.Hostname(), "ip6-localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return fmt.Errorf("collector runtime environment has invalid %s", key)
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

// ValidateSCFEnvironment 校验合并后的完整环境，错误信息里不带任何密钥的值。
func ValidateSCFEnvironment(values map[string]string) error {
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
	return fmt.Errorf("environment is %d bytes; limit is %d (value lengths: %s)", size, SCFEnvironmentLimitBytes, strings.Join(lengths, ", "))
}

// validateCollectorPlanEnvironment 校验定时触发采集函数的环境：携带采集计划，经外部入口访问
// Storage，诊断写 CLS，不连接 EventBus，因此不得出现任何 EventBus 变量。
func validateCollectorPlanEnvironment(values map[string]string) error {
	for key := range values {
		if strings.HasPrefix(key, "MOOX_EVENTBUS_") || key == "MOOX_METRICS_EVENTBUS_URL" || key == "MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE" || key == "NATS_URL" {
			return fmt.Errorf("collector plan environment must not contain EventBus field %s", key)
		}
	}
	if err := ValidateSCFEnvironment(values); err != nil {
		return err
	}
	for _, key := range []string{
		"MOOX_SPACE_ID", "MOOX_CODE_PACKAGE_ID",
		"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_TIMEOUT_MS", "MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY",
	} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector plan environment requires %s", key)
		}
	}
	if err := ValidateCollectorEntryEnvironment(values); err != nil {
		return err
	}
	if enabled, err := strconv.ParseBool(values["MOOX_CLS_ENABLED"]); err != nil || !enabled {
		return fmt.Errorf("collector plan environment requires MOOX_CLS_ENABLED=true")
	}
	if timeout, err := strconv.Atoi(values["MOOX_CLS_TIMEOUT_MS"]); err != nil || timeout <= 0 {
		return fmt.Errorf("collector plan environment has invalid MOOX_CLS_TIMEOUT_MS")
	}
	if len(values[collectorPlanEnvironmentKey]) > 64<<10 {
		return fmt.Errorf("collector plan environment value exceeds 64 KiB")
	}
	return nil
}
