package deriver

import (
	"context"
	"encoding/json"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// TimeSeriesRowsForView projects fact rows into the columns exposed by a view.
func TimeSeriesRowsForView(
	ctx context.Context,
	item *pb.View,
	columns []*pb.ViewColumn,
	rows []*pb.TimeSeriesRow,
	readProjectionRow func(context.Context, *pb.TimeSeriesKey, string) (*pb.TimeSeriesRow, error),
) ([]*pb.TimeSeriesRow, bool, error) {
	if item == nil || !IsProjectableTimeSeriesView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := ViewProjectionDatasets(primaryDatasetID, columns)
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := TimeSeriesProjectionGrainKey(row.GetKey())
		if seen[grainKey] {
			continue
		}
		seen[grainKey] = true
		rowsByDataset := map[string]*pb.TimeSeriesRow{row.GetKey().GetDatasetId(): row}
		for _, datasetID := range datasetIDs {
			if rowsByDataset[datasetID] != nil {
				continue
			}
			read, err := readProjectionRow(ctx, row.GetKey(), datasetID)
			if err != nil {
				return nil, true, err
			}
			if read != nil {
				rowsByDataset[datasetID] = read
			}
		}
		primaryRow := rowsByDataset[primaryDatasetID]
		if primaryRow == nil {
			continue
		}
		out = append(out, &pb.TimeSeriesRow{
			Key:        proto.Clone(primaryRow.GetKey()).(*pb.TimeSeriesKey),
			Columns:    ProjectColumnsForView(primaryDatasetID, columns, rowsByDataset),
			Attributes: CloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, true, nil
}

// RecordRowsForView projects record rows into the columns exposed by a view.
func RecordRowsForView(
	ctx context.Context,
	item *pb.View,
	columns []*pb.ViewColumn,
	rows []*pb.RecordRow,
	readProjectionRow func(context.Context, *pb.RecordKey, string) (*pb.RecordRow, error),
) ([]*pb.RecordRow, bool, error) {
	if item == nil || !IsProjectableRecordView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := ViewProjectionDatasets(primaryDatasetID, columns)
	out := make([]*pb.RecordRow, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := RecordProjectionGrainKey(row.GetKey())
		if seen[grainKey] {
			continue
		}
		seen[grainKey] = true
		rowsByDataset := map[string]*pb.RecordRow{row.GetKey().GetDatasetId(): row}
		for _, datasetID := range datasetIDs {
			if rowsByDataset[datasetID] != nil {
				continue
			}
			read, err := readProjectionRow(ctx, row.GetKey(), datasetID)
			if err != nil {
				return nil, true, err
			}
			if read != nil {
				rowsByDataset[datasetID] = read
			}
		}
		primaryRow := rowsByDataset[primaryDatasetID]
		if primaryRow == nil {
			continue
		}
		out = append(out, &pb.RecordRow{
			Key:        proto.Clone(primaryRow.GetKey()).(*pb.RecordKey),
			Columns:    ProjectRecordColumnsForView(primaryDatasetID, columns, rowsByDataset),
			Attributes: CloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, true, nil
}

// IsProjectableTimeSeriesView reports whether the view can be projected from fact rows.
func IsProjectableTimeSeriesView(item *pb.View, columns []*pb.ViewColumn) bool {
	if item == nil || item.GetPrimaryDatasetId() == "" {
		return false
	}
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			return false
		}
		datasetID := ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column)
		if datasetID == "" {
			return false
		}
	}
	return true
}

// IsProjectableRecordView reports whether the view can be projected from record rows.
func IsProjectableRecordView(item *pb.View, columns []*pb.ViewColumn) bool {
	if item == nil || item.GetPrimaryDatasetId() == "" {
		return false
	}
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			return false
		}
		if ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == "" {
			return false
		}
	}
	return true
}

// ProjectColumnsForView projects time-series row columns into view column names.
func ProjectColumnsForView(primaryDatasetID string, columns []*pb.ViewColumn, rowsByDataset map[string]*pb.TimeSeriesRow) []*pb.ColumnValue {
	valuesByDataset := make(map[string]map[string]*pb.ColumnValue, len(rowsByDataset))
	for datasetID, row := range rowsByDataset {
		values := make(map[string]*pb.ColumnValue, len(row.GetColumns()))
		for _, column := range row.GetColumns() {
			values[column.GetColumnName()] = column
		}
		valuesByDataset[datasetID] = values
	}
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, viewColumn := range columns {
		datasetID := ViewColumnOriginDataset(primaryDatasetID, viewColumn)
		sourceName := ViewColumnSourceName(datasetID, viewColumn)
		source, ok := valuesByDataset[datasetID][sourceName]
		if !ok {
			out = append(out, &pb.ColumnValue{ColumnName: viewColumn.GetColumnName(), ValueType: viewColumn.GetValueType()})
			continue
		}
		copied := proto.Clone(source).(*pb.ColumnValue)
		copied.ColumnName = viewColumn.GetColumnName()
		if copied.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			copied.ValueType = viewColumn.GetValueType()
		}
		out = append(out, copied)
	}
	return out
}

// ProjectRecordColumnsForView projects record row columns into view column names.
func ProjectRecordColumnsForView(primaryDatasetID string, columns []*pb.ViewColumn, rowsByDataset map[string]*pb.RecordRow) []*pb.ColumnValue {
	valuesByDataset := make(map[string]map[string]*pb.ColumnValue, len(rowsByDataset))
	for datasetID, row := range rowsByDataset {
		values := make(map[string]*pb.ColumnValue, len(row.GetColumns()))
		for _, column := range row.GetColumns() {
			values[column.GetColumnName()] = column
		}
		valuesByDataset[datasetID] = values
	}
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, viewColumn := range columns {
		datasetID := ViewColumnOriginDataset(primaryDatasetID, viewColumn)
		sourceName := ViewColumnSourceName(datasetID, viewColumn)
		source, ok := valuesByDataset[datasetID][sourceName]
		if !ok {
			out = append(out, &pb.ColumnValue{ColumnName: viewColumn.GetColumnName(), ValueType: viewColumn.GetValueType()})
			continue
		}
		copied := proto.Clone(source).(*pb.ColumnValue)
		copied.ColumnName = viewColumn.GetColumnName()
		if copied.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			copied.ValueType = viewColumn.GetValueType()
		}
		out = append(out, copied)
	}
	return out
}

// ViewProjectionDatasets returns the datasets needed to project a view.
func ViewProjectionDatasets(primaryDatasetID string, columns []*pb.ViewColumn) []string {
	seen := make(map[string]bool, len(columns)+1)
	out := make([]string, 0, len(columns)+1)
	add := func(datasetID string) {
		if datasetID == "" || seen[datasetID] {
			return
		}
		seen[datasetID] = true
		out = append(out, datasetID)
	}
	add(primaryDatasetID)
	for _, column := range columns {
		add(ViewColumnOriginDataset(primaryDatasetID, column))
	}
	return out
}

// ViewColumnOriginDataset returns the source dataset for a view column.
func ViewColumnOriginDataset(primaryDatasetID string, column *pb.ViewColumn) string {
	if column.GetOriginType() == pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		if idx := strings.LastIndex(column.GetOriginId(), "."); idx > 0 {
			return column.GetOriginId()[:idx]
		}
	}
	return primaryDatasetID
}

// ViewColumnSourceName returns the source column name for a view column.
func ViewColumnSourceName(datasetID string, column *pb.ViewColumn) string {
	originID := column.GetOriginId()
	prefix := datasetID + "."
	if strings.HasPrefix(originID, prefix) {
		return strings.TrimPrefix(originID, prefix)
	}
	if idx := strings.LastIndex(originID, "."); idx >= 0 {
		return originID[idx+1:]
	}
	if originID != "" {
		return originID
	}
	return column.GetColumnName()
}

// TimeSeriesProjectionGrainKey returns the view projection dedupe key for a time-series row key.
func TimeSeriesProjectionGrainKey(key *pb.TimeSeriesKey) string {
	dimensions, _ := json.Marshal(key.GetDimensions())
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetSubjectId(),
		key.GetFreq(),
		key.GetDataTime(),
		string(dimensions),
	}, "\x00")
}

// RecordProjectionGrainKey returns the view projection dedupe key for a record row key.
func RecordProjectionGrainKey(key *pb.RecordKey) string {
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetRecordId(),
		key.GetVersion(),
	}, "\x00")
}

// CloneStringMap clones a string map.
func CloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
