package resample

import (
	"fmt"
	"strings"
)

const maxTargetDatasetIDLength = 25

// DefaultTargetDatasetID returns the frequency-specific derived K-line Dataset ID.
func DefaultTargetDatasetID(marketType, frequencySlug string) string {
	return strings.ToLower(strings.TrimSpace(marketType)) + "_kline_derived_" + frequencySlug
}

// DefaultTargetViewID returns the View ID owned by a derived K-line Dataset.
func DefaultTargetViewID(targetDatasetID string) string {
	return targetDatasetID + "_view"
}

// ValidateTargetDatasetID validates the user-selectable target Dataset name.
func ValidateTargetDatasetID(datasetID, frequencySlug string) error {
	if datasetID == "" {
		return fmt.Errorf("target dataset ID is required")
	}
	if len(datasetID) > maxTargetDatasetIDLength {
		return fmt.Errorf("target dataset ID must not exceed %d characters", maxTargetDatasetIDLength)
	}
	if !isLowerSnakeID(datasetID) {
		return fmt.Errorf("target dataset ID must use lower snake case")
	}
	frequency, err := ParseFixedFrequency(frequencySlug)
	if err != nil || frequency.Slug != frequencySlug {
		return fmt.Errorf("target dataset frequency suffix is invalid")
	}
	if !strings.HasSuffix(datasetID, "_"+frequencySlug) {
		return fmt.Errorf("target dataset ID must end with _%s", frequencySlug)
	}
	return nil
}

func isLowerSnakeID(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' || value[len(value)-1] == '_' {
		return false
	}
	previousUnderscore := false
	for _, char := range value {
		if char == '_' {
			if previousUnderscore {
				return false
			}
			previousUnderscore = true
			continue
		}
		previousUnderscore = false
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}
