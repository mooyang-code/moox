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
	"unicode"
)

const SCFEnvironmentLimitBytes = 4096
const CollectorTimerTimeoutSeconds = 60

var accessInstanceName = regexp.MustCompile(`^access@[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// ValidateCollectorTimerEnvironment is shared by publication and merged
// CloudNode deployment, so partial deploy patches cannot bypass create gates.
func ValidateCollectorTimerEnvironment(values map[string]string) error {
	return ValidateCollectorMarketFetchEnvironment(values)
}

// ValidateCollectorAccessEnvironment validates the five fields shared by
// native Claim and Storage calls, without disclosing credential contents.
func ValidateCollectorAccessEnvironment(values map[string]string) error {
	for _, key := range []string{"MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector Access environment requires %s", key)
		}
	}
	if values["MOOX_CALLER"] != "scf-collector" {
		return fmt.Errorf("collector Access environment requires MOOX_CALLER=scf-collector")
	}
	if len(values["MOOX_ACCESS_ID"]) > 135 || !accessInstanceName.MatchString(values["MOOX_ACCESS_ID"]) {
		return fmt.Errorf("collector Access environment has invalid MOOX_ACCESS_ID")
	}
	if err := validateCollectorRuntimeEndpoint("MOOX_ACCESS_ADDRESS", "ip://"+values["MOOX_ACCESS_ADDRESS"], "ip"); err != nil {
		return err
	}
	keyID := values["MOOX_CALLER_KEY_ID"]
	if len(keyID) > 128 || strings.ContainsAny(keyID, "/\\") || strings.ContainsFunc(keyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("collector Access environment has invalid MOOX_CALLER_KEY_ID")
	}
	key := values["MOOX_CALLER_KEY"]
	if len(key) < 32 || len(key) > 4096 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("collector Access environment has invalid MOOX_CALLER_KEY")
	}
	return nil
}

// RemoveCollectorInternalGatewayEnvironment clears internal credentials and
// addresses when a function is published with the external Access contract.
func RemoveCollectorInternalGatewayEnvironment(values map[string]string) bool {
	changed := false
	for _, key := range collectorInternalGatewayEnvironmentKeys {
		if _, ok := values[key]; ok {
			delete(values, key)
			changed = true
		}
	}
	return changed
}

var collectorInternalGatewayEnvironmentKeys = [...]string{
	"MOOX_STORAGE_RPC_GATEWAY_TARGET", "MOOX_GATEWAY_NODE_ID", "MOOX_GATEWAY_TARGET_NODE",
	"MOOX_GATEWAY_CALLER", "MOOX_GATEWAY_SERVICE_KEY_ID", "MOOX_GATEWAY_SERVICE_SECRET_KEY",
	"MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "MOOX_COLLECTOR_NODE_ID",
	"MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY",
	"MOOX_RPC_SERVICE_ID", "MOOX_RPC_SERVICE_SECRET", "MOOX_GATEWAY_CA_FILE", "MOOX_GATEWAY_CA_PEM_B64",
	"MOOX_SERVICE_GATEWAY_CA_FILE", "MOOX_SERVICE_GATEWAY_CA_PEM_B64",
}

// ValidateCollectorMarketFetchEnvironment checks common Invoke and Timer
// dependencies. Claim and Storage use the same Access identity and connection.
func ValidateCollectorMarketFetchEnvironment(values map[string]string) error {
	for _, key := range collectorInternalGatewayEnvironmentKeys {
		if _, ok := values[key]; ok {
			return fmt.Errorf("collector Access environment must not contain internal gateway field %s", key)
		}
	}
	if err := ValidateSCFEnvironment(values); err != nil {
		return err
	}
	for _, key := range []string{
		"MOOX_SPACE_ID", "MOOX_CODE_PACKAGE_ID",
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON",
		"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_TIMEOUT_MS",
		"MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY",
		"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE",
	} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector market-fetch runtime environment requires %s", key)
		}
	}
	if err := validateStorageAppKeys(values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"]); err != nil {
		return fmt.Errorf("collector market-fetch runtime environment has invalid MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON: %w", err)
	}
	if err := ValidateCollectorAccessEnvironment(values); err != nil {
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

func validateStorageAppKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		appID, ok := token.(string)
		if err != nil || !ok || strings.TrimSpace(appID) == "" {
			return fmt.Errorf("invalid app ID")
		}
		if _, duplicate := seen[appID]; duplicate {
			return fmt.Errorf("duplicate app ID")
		}
		seen[appID] = struct{}{}
		var key string
		if err := decoder.Decode(&key); err != nil || len(key) != 64 {
			return fmt.Errorf("app keys must be 64 hex characters")
		}
		if _, err := hex.DecodeString(key); err != nil {
			return fmt.Errorf("app keys must be 64 hex characters")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid app-key object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	if len(seen) == 0 {
		return fmt.Errorf("app keys must not be empty")
	}
	if _, ok := seen["moox-collector"]; !ok {
		return fmt.Errorf("app keys must include the moox-collector binding")
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

// ValidateSCFEnvironment checks the complete merged environment without exposing
// credential values in a release or CloudNode error.
func ValidateSCFEnvironment(values map[string]string) error {
	if value, exists := values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"]; exists {
		return fmt.Errorf("SCF environment must not contain MOOX_STORAGE_PRIMARY_AUTH_SECRET (value length: %d)", len(value))
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
	return fmt.Errorf("environment is %d bytes; limit is %d (value lengths: %s)", size, SCFEnvironmentLimitBytes, strings.Join(lengths, ", "))
}
