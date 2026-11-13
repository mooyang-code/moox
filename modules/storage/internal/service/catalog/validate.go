package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var lowerSnakeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxChineseDisplayNameRunes = 10

var reservedTimeSeriesSystemColumns = map[string]struct{}{
	"subject_id": {},
	"freq":       {},
	"data_time":  {},
	"series_tag": {},
}

func defaultViewGrainKeys(kind pb.DataKind) []string {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		return []string{"subject_id", "freq", "data_time", "series_tag"}
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
	if !strings.HasPrefix(datasetID, "dataset_") {
		return errors.New("dataset_id must start with dataset_")
	}
	return validateLowerSnakeID("dataset_id", datasetID, 50)
}

func validateViewID(viewID string) error {
	if !strings.HasPrefix(viewID, "view_") {
		return errors.New("view_id must start with view_")
	}
	// Collector View IDs include the complete tag, task type, and frequency.
	// Tag IDs may be 64 characters, so the View limit must leave room for those
	// semantic components instead of rejecting otherwise valid tags at create.
	return validateLowerSnakeID("view_id", viewID, 128)
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

// validateColumnDisplayName 要求列带显示名；显示名由建列的模块写入，Storage 不推断、不按业务规则校验。
func validateColumnDisplayName(field string, attrs map[string]string) error {
	if strings.TrimSpace(attrs["display_name"]) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
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

func validateUserColumnName(field string, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, reserved := reservedTimeSeriesSystemColumns[name]; reserved {
		return fmt.Errorf("%s %q is a reserved system column", field, name)
	}
	return nil
}

func viewDatasetID(view *pb.View) string {
	if view == nil {
		return ""
	}
	return strings.TrimSpace(view.GetDatasetId())
}
