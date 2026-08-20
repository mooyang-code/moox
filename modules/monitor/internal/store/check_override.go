package store

import "encoding/json"

// EnabledOverrideLabel records an explicit user choice for a system-deployed
// check. The absence of the label means that deployment sync owns the default
// (enabled) state; only an explicit disable needs to be persisted.
const EnabledOverrideLabel = "monitor_enabled_override"

// CheckEnabledOverride returns the explicit enabled value, when one exists.
func CheckEnabledOverride(labels string) (bool, bool) {
	var values map[string]any
	if json.Unmarshal([]byte(labels), &values) != nil {
		return false, false
	}
	raw, ok := values[EnabledOverrideLabel]
	if !ok {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		if value == "true" {
			return true, true
		}
		if value == "false" {
			return false, true
		}
	}
	return false, false
}

// SetCheckEnabledOverride adds or removes the explicit user override while
// retaining the deployment labels used to identify the service instance.
func SetCheckEnabledOverride(labels string, enabled bool) string {
	values := make(map[string]any)
	if json.Unmarshal([]byte(labels), &values) != nil || values == nil {
		values = make(map[string]any)
	}
	if enabled {
		delete(values, EnabledOverrideLabel)
	} else {
		values[EnabledOverrideLabel] = false
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return labels
	}
	return string(encoded)
}
