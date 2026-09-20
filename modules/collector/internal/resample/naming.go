package resample

import (
	"fmt"
	"strings"
)

const maxTargetDatasetIDLength = 50

// ValidateTargetDatasetID validates the internal task-owned result Dataset ID.
// The variadic argument is intentionally ignored: result identity belongs to
// the task, never to a frequency-specific naming suffix.
func ValidateTargetDatasetID(datasetID string, _ ...string) error {
	if datasetID == "" {
		return fmt.Errorf("target dataset ID is required")
	}
	if len(datasetID) > maxTargetDatasetIDLength {
		return fmt.Errorf("target dataset ID must not exceed %d characters", maxTargetDatasetIDLength)
	}
	if !isLowerSnakeID(datasetID) {
		return fmt.Errorf("target dataset ID must use lower snake case")
	}
	if !strings.HasPrefix(datasetID, "dataset_") {
		return fmt.Errorf("target dataset ID must start with dataset_")
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
