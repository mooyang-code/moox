package access

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

var lowerSnakeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxChineseDisplayNameRunes = 10

func datasetSupportsFreq(dataset *pb.Dataset, freq string) bool {
	for _, item := range dataset.GetFreqs() {
		if strings.TrimSpace(item) == freq {
			return true
		}
	}
	return false
}

func defaultViewGrainKeys(kind pb.DataKind) []string {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		return []string{"subject_id", "freq", "data_time"}
	}
	return []string{"record_id", "version"}
}

func defaultViewEngine(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		return "duckdb"
	}
	return "bleve"
}

func validateDatasetID(datasetID string) error {
	return validateLowerSnakeID("dataset_id", datasetID, 20)
}

func validateViewID(viewID string) error {
	return validateLowerSnakeID("view_id", viewID, 30)
}

func validateChineseDisplayName(field string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxChineseDisplayNameRunes {
		return fmt.Errorf("%s must be <= %d characters", field, maxChineseDisplayNameRunes)
	}
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return nil
		}
	}
	return fmt.Errorf("%s must contain Chinese characters", field)
}

func validateColumnDisplayName(field string, attrs map[string]string) error {
	if attrs == nil {
		return validateChineseDisplayName(field, "")
	}
	return validateChineseDisplayName(field, attrs["display_name"])
}

func validateLowerSnakeID(field string, value string, maxLen int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s length must be <= %d", field, maxLen)
	}
	if !lowerSnakeIDPattern.MatchString(value) {
		return fmt.Errorf("%s must use lower snake case letters, digits and underscores", field)
	}
	return nil
}

func validateViewColumnName(column *pb.ViewColumn) error {
	if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		return nil
	}
	originID := strings.TrimSpace(column.GetOriginId())
	columnName := strings.TrimSpace(column.GetColumnName())
	datasetID, sourceName, ok := strings.Cut(originID, ".")
	if !ok || datasetID == "" || sourceName == "" {
		return errors.New("dataset view column origin_id must use dataset_id.column_name")
	}
	if err := validateDatasetID(datasetID); err != nil {
		return fmt.Errorf("invalid view column origin dataset: %w", err)
	}
	if columnName != originID {
		return errors.New("dataset view column column_name must equal origin_id and use dataset_id.column_name")
	}
	return nil
}

func normalizeViewDatasetIDs(primaryDatasetID string, datasetIDs []string) []string {
	seen := make(map[string]bool, len(datasetIDs)+1)
	out := make([]string, 0, len(datasetIDs)+1)
	add := func(datasetID string) {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" || seen[datasetID] {
			return
		}
		seen[datasetID] = true
		out = append(out, datasetID)
	}
	add(primaryDatasetID)
	for _, datasetID := range datasetIDs {
		add(datasetID)
	}
	return out
}
