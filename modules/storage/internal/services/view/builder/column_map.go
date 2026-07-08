package builder

import (
	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// MapDatasetColumnsToView maps columns from one dataset write batch into view column names.
// It never reads other datasets.
func MapDatasetColumnsToView(view *pb.View, columns []*pb.ViewColumn, datasetID string, rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	if view == nil || datasetID == "" || len(rows) == 0 {
		return nil
	}
	mappings := timeSeriesColumnMappings(view.GetPrimaryDatasetId(), columns, datasetID)
	if len(mappings) == 0 {
		return nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		values := columnValuesByName(row.GetColumns())
		patch := &pb.TimeSeriesRow{
			Key:        proto.Clone(row.GetKey()).(*pb.TimeSeriesKey),
			Attributes: cloneBuilderStringMap(row.GetAttributes()),
		}
		if view.GetPrimaryDatasetId() != "" {
			patch.Key.DatasetId = view.GetPrimaryDatasetId()
		}
		for _, mapping := range mappings {
			source := values[mapping.sourceName]
			if source == nil {
				continue
			}
			copied := proto.Clone(source).(*pb.ColumnValue)
			copied.ColumnName = mapping.viewName
			if copied.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
				copied.ValueType = mapping.valueType
			}
			patch.Columns = append(patch.Columns, copied)
		}
		if len(patch.Columns) > 0 {
			out = append(out, patch)
		}
	}
	return out
}

// MapRecordColumnsToView maps one record dataset write batch into record view columns.
// It never reads other datasets.
func MapRecordColumnsToView(view *pb.View, columns []*pb.ViewColumn, datasetID string, rows []*pb.RecordRow) []*pb.RecordRow {
	if view == nil || datasetID == "" || len(rows) == 0 {
		return nil
	}
	mappings := timeSeriesColumnMappings(view.GetPrimaryDatasetId(), columns, datasetID)
	if len(mappings) == 0 {
		return nil
	}
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		values := columnValuesByName(row.GetColumns())
		patch := &pb.RecordRow{
			Key:        proto.Clone(row.GetKey()).(*pb.RecordKey),
			Attributes: cloneBuilderStringMap(row.GetAttributes()),
		}
		if view.GetPrimaryDatasetId() != "" {
			patch.Key.DatasetId = view.GetPrimaryDatasetId()
		}
		for _, mapping := range mappings {
			source := values[mapping.sourceName]
			if source == nil {
				continue
			}
			copied := proto.Clone(source).(*pb.ColumnValue)
			copied.ColumnName = mapping.viewName
			if copied.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
				copied.ValueType = mapping.valueType
			}
			patch.Columns = append(patch.Columns, copied)
		}
		if len(patch.Columns) > 0 {
			out = append(out, patch)
		}
	}
	return out
}

type viewColumnMapping struct {
	sourceName string
	viewName   string
	valueType  pb.FieldValueType
}

func timeSeriesColumnMappings(primaryDatasetID string, columns []*pb.ViewColumn, datasetID string) []viewColumnMapping {
	out := make([]viewColumnMapping, 0, len(columns))
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			continue
		}
		if viewsvc.ViewColumnOriginDataset(primaryDatasetID, column) != datasetID {
			continue
		}
		out = append(out, viewColumnMapping{
			sourceName: viewsvc.ViewColumnSourceName(datasetID, column),
			viewName:   column.GetColumnName(),
			valueType:  column.GetValueType(),
		})
	}
	return out
}

func hasUnsupportedSteadyColumns(columns []*pb.ViewColumn) bool {
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			return true
		}
	}
	return false
}

func columnValuesByName(columns []*pb.ColumnValue) map[string]*pb.ColumnValue {
	out := make(map[string]*pb.ColumnValue, len(columns))
	for _, column := range columns {
		if column == nil {
			continue
		}
		out[column.GetColumnName()] = column
	}
	return out
}

func cloneBuilderStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
