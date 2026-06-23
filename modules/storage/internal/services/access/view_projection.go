package access

import (
	"context"
	"encoding/json"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) timeSeriesRowsForView(ctx context.Context, item *pb.View, columns []*pb.ViewColumn, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, bool, error) {
	if item == nil || !isProjectableTimeSeriesView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := viewProjectionDatasets(primaryDatasetID, columns)
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := timeSeriesProjectionGrainKey(row.GetKey())
		if seen[grainKey] {
			continue
		}
		seen[grainKey] = true
		rowsByDataset := map[string]*pb.TimeSeriesRow{row.GetKey().GetDatasetId(): row}
		for _, datasetID := range datasetIDs {
			if rowsByDataset[datasetID] != nil {
				continue
			}
			read, err := s.readTimeSeriesProjectionRow(ctx, row.GetKey(), datasetID)
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
			Columns:    projectColumnsForView(primaryDatasetID, columns, rowsByDataset),
			Attributes: cloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, true, nil
}

func (s *Service) readTimeSeriesProjectionRow(ctx context.Context, base *pb.TimeSeriesKey, datasetID string) (*pb.TimeSeriesRow, error) {
	key := proto.Clone(base).(*pb.TimeSeriesKey)
	key.DatasetId = datasetID
	rsp, err := s.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errText(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}

func isProjectableTimeSeriesView(item *pb.View, columns []*pb.ViewColumn) bool {
	if item == nil || item.GetPrimaryDatasetId() == "" {
		return false
	}
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			return false
		}
		datasetID := viewColumnOriginDataset(item.GetPrimaryDatasetId(), column)
		if datasetID == "" {
			return false
		}
	}
	return true
}

func projectColumnsForView(primaryDatasetID string, columns []*pb.ViewColumn, rowsByDataset map[string]*pb.TimeSeriesRow) []*pb.ColumnValue {
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
		datasetID := viewColumnOriginDataset(primaryDatasetID, viewColumn)
		sourceName := viewColumnSourceName(datasetID, viewColumn)
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

func viewProjectionDatasets(primaryDatasetID string, columns []*pb.ViewColumn) []string {
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
		add(viewColumnOriginDataset(primaryDatasetID, column))
	}
	return out
}

func viewColumnOriginDataset(primaryDatasetID string, column *pb.ViewColumn) string {
	if column.GetOriginType() == pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		if idx := strings.LastIndex(column.GetOriginId(), "."); idx > 0 {
			return column.GetOriginId()[:idx]
		}
	}
	return primaryDatasetID
}

func viewColumnSourceName(datasetID string, column *pb.ViewColumn) string {
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

func (s *Service) recordRowsForView(ctx context.Context, item *pb.View, columns []*pb.ViewColumn, rows []*pb.RecordRow) ([]*pb.RecordRow, bool, error) {
	if item == nil || !isProjectableRecordView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := viewProjectionDatasets(primaryDatasetID, columns)
	out := make([]*pb.RecordRow, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := recordProjectionGrainKey(row.GetKey())
		if seen[grainKey] {
			continue
		}
		seen[grainKey] = true
		rowsByDataset := map[string]*pb.RecordRow{row.GetKey().GetDatasetId(): row}
		for _, datasetID := range datasetIDs {
			if rowsByDataset[datasetID] != nil {
				continue
			}
			read, err := s.readRecordProjectionRow(ctx, row.GetKey(), datasetID)
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
			Columns:    projectRecordColumnsForView(primaryDatasetID, columns, rowsByDataset),
			Attributes: cloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, true, nil
}

func (s *Service) readRecordProjectionRow(ctx context.Context, base *pb.RecordKey, datasetID string) (*pb.RecordRow, error) {
	key := proto.Clone(base).(*pb.RecordKey)
	key.DatasetId = datasetID
	rsp, err := s.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errText(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}

func projectRecordColumnsForView(primaryDatasetID string, columns []*pb.ViewColumn, rowsByDataset map[string]*pb.RecordRow) []*pb.ColumnValue {
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
		datasetID := viewColumnOriginDataset(primaryDatasetID, viewColumn)
		sourceName := viewColumnSourceName(datasetID, viewColumn)
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

func isProjectableRecordView(item *pb.View, columns []*pb.ViewColumn) bool {
	if item == nil || item.GetPrimaryDatasetId() == "" {
		return false
	}
	for _, column := range columns {
		if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			return false
		}
		if viewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == "" {
			return false
		}
	}
	return true
}

func timeSeriesProjectionGrainKey(key *pb.TimeSeriesKey) string {
	dimensions, _ := json.Marshal(key.GetDimensions())
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetSubjectId(),
		key.GetFreq(),
		key.GetDataTime(),
		string(dimensions),
	}, "\x00")
}

func recordProjectionGrainKey(key *pb.RecordKey) string {
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetRecordId(),
		key.GetVersion(),
	}, "\x00")
}
