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

var collectorNodeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// ValidateCollectorTimerEnvironment is shared by publication and merged
// CloudNode deployment, so partial deploy patches cannot bypass create gates.
func ValidateCollectorTimerEnvironment(values map[string]string) error {
	if err := ValidateSCFEnvironment(values); err != nil {
		return err
	}
	for _, key := range []string{
		"MOOX_SPACE_ID", "MOOX_CODE_PACKAGE_ID",
		"MOOX_GATEWAY_NODE_ID", "MOOX_GATEWAY_TARGET_NODE",
		"MOOX_GATEWAY_SERVICE_KEY_ID", "MOOX_GATEWAY_SERVICE_SECRET_KEY",
		"MOOX_GATEWAY_CALLER", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON",
		"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_TIMEOUT_MS",
		"MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY",
		"MOOX_STORAGE_RPC_GATEWAY_TARGET", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE",
		"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE",
	} {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("collector Timer runtime environment requires %s", key)
		}
	}
	if err := validateStorageAppKeys(values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"]); err != nil {
		return fmt.Errorf("collector Timer runtime environment has invalid MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON: %w", err)
	}
	for _, key := range []string{"MOOX_STORAGE_RPC_GATEWAY_TARGET", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "MOOX_EVENTBUS_NATS_URL"} {
		scheme := "ip"
		if key == "MOOX_EVENTBUS_NATS_URL" {
			scheme = "tls"
		}
		parsed, err := url.Parse(strings.TrimSpace(values[key]))
		if err != nil || parsed.Scheme != scheme || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("collector Timer runtime environment requires %s as %s://host:port", key, scheme)
		}
		port, err := strconv.Atoi(parsed.Port())
		ip := net.ParseIP(parsed.Hostname())
		if err != nil || port < 1 || port > 65535 || strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "localhost") || strings.EqualFold(parsed.Hostname(), "ip6-localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
			return fmt.Errorf("collector Timer runtime environment has invalid %s", key)
		}
	}
	for _, key := range []string{"MOOX_GATEWAY_NODE_ID", "MOOX_GATEWAY_TARGET_NODE", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE"} {
		if !collectorNodeName.MatchString(strings.TrimSpace(values[key])) {
			return fmt.Errorf("collector Timer runtime environment has invalid %s", key)
		}
	}
	if enabled, err := strconv.ParseBool(values["MOOX_CLS_ENABLED"]); err != nil || !enabled {
		return fmt.Errorf("collector Timer runtime environment requires MOOX_CLS_ENABLED=true")
	}
	if timeout, err := strconv.Atoi(values["MOOX_CLS_TIMEOUT_MS"]); err != nil || timeout <= 0 {
		return fmt.Errorf("collector Timer runtime environment has invalid MOOX_CLS_TIMEOUT_MS")
	}
	if values["MOOX_EVENTBUS_NATS_TLS_CA_FILE"] != "certs/eventbus-ca.pem" {
		return fmt.Errorf("collector Timer runtime environment requires packaged MOOX_EVENTBUS_NATS_TLS_CA_FILE")
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
