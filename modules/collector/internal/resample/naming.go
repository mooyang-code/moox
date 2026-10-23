package resample

import (
	"fmt"
	"strings"
)

const maxTargetDatasetIDLength = 50

// ValidateTargetDatasetID 校验任务自有的结果 Dataset ID：结果身份属于任务，不带频率后缀。
func ValidateTargetDatasetID(datasetID string) error {
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
