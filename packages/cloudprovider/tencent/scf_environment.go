package tencent

import (
	"fmt"
	"sort"
	"strings"
)

const SCFEnvironmentLimitBytes = 4096
const CollectorTimerTimeoutSeconds = 60

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
