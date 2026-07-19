package view

import (
	"encoding/json"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fixedViewFilter struct {
	SpaceID    string            `json:"space_id"`
	SubjectID  string            `json:"subject_id"`
	Freq       string            `json:"freq"`
	Dimensions map[string]string `json:"dimensions"`
}

func parseFixedViewFilter(raw string) (*fixedViewFilter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var filter fixedViewFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return nil, fmt.Errorf("invalid view filter_json: %w", err)
	}
	if filter.SpaceID == "" && filter.SubjectID == "" && filter.Freq == "" && len(filter.Dimensions) == 0 {
		return nil, nil
	}
	return &filter, nil
}

func filterRowsByViewJSON(view *pb.View, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, error) {
	filter, err := parseFixedViewFilter(view.GetFilterJson())
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return rows, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if fixedViewFilterMatchesRow(filter, row) {
			out = append(out, row)
		}
	}
	return out, nil
}

func fixedViewFilterMatchesRow(filter *fixedViewFilter, row *pb.TimeSeriesRow) bool {
	if row == nil || row.GetKey() == nil {
		return false
	}
	key := row.GetKey()
	if filter.SpaceID != "" && filter.SpaceID != key.GetSpaceId() {
		return false
	}
	if filter.SubjectID != "" && filter.SubjectID != key.GetSubjectId() {
		return false
	}
	if filter.Freq != "" && !strings.EqualFold(filter.Freq, key.GetFreq()) {
		return false
	}
	for name, value := range filter.Dimensions {
		if key.GetDimensions()[name] != value {
			return false
		}
	}
	return true
}
