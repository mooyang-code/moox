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
	return validateLowerSnakeID("view_id", viewID, 50)
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

func validateColumnDisplayName(field string, spaceID string, attrs map[string]string, factorOutputColumn bool) error {
	if attrs == nil {
		return validateChineseDisplayName(field, "")
	}
	displayName := strings.TrimSpace(attrs["display_name"])
	factorOutput := strings.TrimSpace(attrs["factor_output"])
	if factorOutputColumn {
		if displayName != factorOutput {
			return fmt.Errorf("%s must match factor_output", field)
		}
		return nil
	}
	if spaceID == "mooxsys" {
		if displayName == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	return validateChineseDisplayName(field, displayName)
}

func isFactorDatasetColumn(column *pb.DatasetColumn) bool {
	if column == nil || column.GetOriginType() != pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR {
		return false
	}
	attrs := column.GetAttributes()
	factorID := strings.TrimSpace(attrs["origin_factor_id"])
	output := strings.TrimSpace(attrs["factor_output"])
	return factorID != "" && output != "" && strings.TrimSpace(column.GetOriginId()) == factorID+"."+output
}

func isFactorViewColumn(column *pb.ViewColumn) bool {
	if column == nil || column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		return false
	}
	attrs := column.GetAttributes()
	factorID := strings.TrimSpace(attrs["origin_factor_id"])
	output := strings.TrimSpace(attrs["factor_output"])
	originID := strings.TrimSpace(column.GetOriginId())
	return factorID != "" && output != "" && strings.EqualFold(originID, strings.TrimSpace(column.GetColumnName())) &&
		strings.HasSuffix(strings.ToLower(originID), "."+strings.ToLower(factorID)+"__"+strings.ToLower(output))
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
	if err := validateUserColumnName("view column_name", column.GetColumnName()); err != nil {
		return err
	}
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

func validateUserColumnName(field string, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, reserved := reservedTimeSeriesSystemColumns[name]; reserved {
		return fmt.Errorf("%s %q is a reserved system column", field, name)
	}
	return nil
}

func validateViewColumns(columns []*pb.ViewColumn) error {
	for _, column := range columns {
		if column == nil {
			continue
		}
		if err := validateViewColumnName(column); err != nil {
			return err
		}
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
