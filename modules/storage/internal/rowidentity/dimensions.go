package rowidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CanonicalDimensions returns the stable representation used by every
// time-series storage engine as part of row identity.
func CanonicalDimensions(dimensions map[string]string) (string, error) {
	for key := range dimensions {
		if strings.TrimSpace(key) == "" {
			return "", errors.New("dimension names must not be empty")
		}
	}
	raw, err := json.Marshal(dimensions)
	if err != nil {
		return "", fmt.Errorf("marshal dimensions: %w", err)
	}
	if len(dimensions) == 0 {
		return "{}", nil
	}
	return string(raw), nil
}
