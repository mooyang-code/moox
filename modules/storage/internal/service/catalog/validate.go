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

func validateDatasetDataNodeID(dataNodeID string) error {
	if strings.TrimSpace(dataNodeID) == "" {
		return errors.New("data_node_id is required")
	}
	return nil
}

func validateDatasetDataNodeUpdate(existing *pb.Dataset, dataNodeID string) error {
	dataNodeID = strings.TrimSpace(dataNodeID)
	if dataNodeID == "" || existing.GetDataNodeId() == dataNodeID {
		return nil
	}
	if existing.GetStatus() == "active" {
		return errors.New("active dataset data_node_id is immutable")
	}
	return errors.New("dataset data_node_id is immutable; use RebindDatasetDataNode while disabled")
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

const factorResultDatasetPrefix = "dataset_factor_"

// normalizeDatasetRole canonicalises the role attribute in place so that every
// later strict comparison (write guards, default View creation, column sync)
// sees the same value, then enforces the role-specific contract.
func normalizeDatasetRole(dataset *pb.Dataset) error {
	if dataset == nil {
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(dataset.GetAttributes()["dataset_role"]))
	if dataset.GetAttributes() != nil {
		if _, ok := dataset.Attributes["dataset_role"]; ok {
			dataset.Attributes["dataset_role"] = role
		}
	}
	switch role {
	case "merged_factor":
		return errors.New("dataset_role merged_factor is retired; use raw_collection or factor_result")
	case "factor_result":
		// View consumers route result datasets to the ordered factor durable by
		// this prefix, so the naming is part of the role contract.
		if !strings.HasPrefix(dataset.GetDatasetId(), factorResultDatasetPrefix) {
			return fmt.Errorf("factor_result dataset_id must start with %s", factorResultDatasetPrefix)
		}
	}
	return nil
}

func isFactorResultDataset(dataset *pb.Dataset) bool {
	return dataset != nil && strings.EqualFold(strings.TrimSpace(dataset.GetAttributes()["dataset_role"]), "factor_result")
}

// isDatasetViewColumn reports whether a View column reads a Dataset column.
// Metadata stores an unspecified origin type as a dataset column.
func isDatasetViewColumn(column *pb.ViewColumn) bool {
	if column == nil {
		return false
	}
	switch column.GetOriginType() {
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION:
		return false
	}
	return true
}

func isFactorViewColumn(column *pb.ViewColumn) bool {
	if !isDatasetViewColumn(column) {
		return false
	}
	attrs := column.GetAttributes()
	factorID := strings.TrimSpace(attrs["origin_factor_id"])
	output := strings.TrimSpace(attrs["factor_output"])
	// A factor output column is named after its output, like the Dataset
	// column it reads.
	originID := strings.TrimSpace(column.GetOriginId())
	return factorID != "" && output != "" && originID == strings.TrimSpace(column.GetColumnName()) && originID == output
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
	if !isDatasetViewColumn(column) {
		return nil
	}
	// A View indexes exactly one Dataset, so a dataset column is the bare
	// Dataset column name; View.dataset_id already names the source Dataset.
	originID := strings.TrimSpace(column.GetOriginId())
	if originID == "" || strings.Contains(originID, ".") {
		return errors.New("dataset view column origin_id must be the bare Dataset column name")
	}
	if strings.TrimSpace(column.GetColumnName()) != originID {
		return errors.New("dataset view column column_name must equal origin_id")
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

func viewDatasetID(view *pb.View) string {
	if view == nil {
		return ""
	}
	return strings.TrimSpace(view.GetDatasetId())
}
