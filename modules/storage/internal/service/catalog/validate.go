package catalog

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
)

var lowerSnakeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxChineseDisplayNameRunes = 10

var reservedTimeSeriesSystemColumns = map[string]struct{}{
	"subject_id": {},
	"freq":       {},
	"data_time":  {},
	"series_tag": {},
}

// datasetColumnNamePattern 是数据集列名的标识符规则：列名会原样成为 View 索引里的列名。
var datasetColumnNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateDatasetColumnName 拒绝系统列名与不是标识符的列名：View 的列等于数据集的列，
// 系统列名不会建进索引，记下的已建列就会和实际建出的列对不上，之后再也加不了列。
func validateDatasetColumnName(name string) error {
	if !datasetColumnNamePattern.MatchString(strings.TrimSpace(name)) {
		return storeerr.Invalidf("数据集列名 %q 只能由字母、数字和下划线组成，且不能以数字开头", name)
	}
	return validateUserColumnName("dataset column_name", name)
}

func validateDatasetID(datasetID string) error {
	if !strings.HasPrefix(datasetID, "dataset_") {
		return storeerr.Invalidf("dataset_id must start with dataset_")
	}
	return validateLowerSnakeID("dataset_id", datasetID, 50)
}

func validateChineseDisplayName(field string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return storeerr.Invalidf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxChineseDisplayNameRunes {
		return storeerr.Invalidf("%s must be <= %d characters", field, maxChineseDisplayNameRunes)
	}
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return nil
		}
	}
	return storeerr.Invalidf("%s must contain Chinese characters", field)
}

// validateColumnDisplayName 要求列带显示名；显示名由建列的模块写入，Storage 不推断、不按业务规则校验。
func validateColumnDisplayName(field string, displayName string) error {
	if strings.TrimSpace(displayName) == "" {
		return storeerr.Invalidf("%s is required", field)
	}
	return nil
}

func validateLowerSnakeID(field string, value string, maxLen int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return storeerr.Invalidf("%s is required", field)
	}
	if len(value) > maxLen {
		return storeerr.Invalidf("%s length must be <= %d", field, maxLen)
	}
	if !lowerSnakeIDPattern.MatchString(value) {
		return storeerr.Invalidf("%s must use lower snake case letters, digits and underscores", field)
	}
	return nil
}

func validateUserColumnName(field string, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, reserved := reservedTimeSeriesSystemColumns[name]; reserved {
		return storeerr.Invalidf("%s %q is a reserved system column", field, name)
	}
	return nil
}
