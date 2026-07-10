package view

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

const projectionReadBatchSize = 500

type TimeSeriesProjectionReader func(context.Context, []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error)

type RecordProjectionReader func(context.Context, []*pb.RecordKey) ([]*pb.RecordRow, error)

// BuildCurrentRecordMutation builds one complete CURRENT document from rows
// captured at the same source snapshot boundary.
func BuildCurrentRecordMutation(item *pb.View, rowsByDataset map[string]*pb.RecordRow, sourceID string, projectionWatermark uint64) (*pb.RecordIndexMutation, error) {
	if item == nil || item.GetPrimaryDatasetId() == "" {
		return nil, errors.New("Record View and primary dataset are required")
	}
	if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY {
		return nil, errors.New("CURRENT mutation cannot use HISTORY Record View")
	}
	primary := rowsByDataset[item.GetPrimaryDatasetId()]
	if primary == nil {
		return nil, nil
	}
	key := proto.Clone(primary.GetKey()).(*pb.RecordKey)
	row := &pb.RecordRow{Key: key, Columns: ProjectRecordColumnsForView(item.GetPrimaryDatasetId(), item.GetColumns(), rowsByDataset), Attributes: CloneStringMap(primary.GetAttributes()), Revision: primary.GetRevision(), UpdatedAt: primary.GetUpdatedAt(), CommitSeq: primary.GetCommitSeq()}
	return &pb.RecordIndexMutation{Row: row, OrderCommitSeq: projectionWatermark, SourceId: sourceID}, nil
}

// BuildHistoryRecordMutation maps the committed immutable row directly. It
// never rereads CURRENT, so every revision remains independently indexable.
func BuildHistoryRecordMutation(item *pb.View, committed *pb.RecordRow, sourceID string) (*pb.RecordIndexMutation, error) {
	if item == nil || item.GetPrimaryDatasetId() == "" || committed == nil || committed.GetKey() == nil {
		return nil, errors.New("history Record mutation requires a committed primary row")
	}
	if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT || len(item.GetDatasetIds()) > 1 {
		return nil, errors.New("HISTORY mutation requires one dataset")
	}
	key := proto.Clone(committed.GetKey()).(*pb.RecordKey)
	row := proto.Clone(committed).(*pb.RecordRow)
	row.Key = key
	return &pb.RecordIndexMutation{Row: row, OrderCommitSeq: committed.GetCommitSeq(), SourceId: sourceID}, nil
}

// TimeSeriesRowsForView projects fact rows into the columns exposed by a view.
func TimeSeriesRowsForView(
	ctx context.Context,
	item *pb.View,
	columns []*pb.ViewColumn,
	rows []*pb.TimeSeriesRow,
	readProjectionRows TimeSeriesProjectionReader,
) ([]*pb.TimeSeriesRow, bool, error) {
	if item == nil || !IsProjectableTimeSeriesView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := ViewProjectionDatasets(primaryDatasetID, columns)
	type projectionGroup struct {
		template      *pb.TimeSeriesKey
		rowsByDataset map[string]*pb.TimeSeriesRow
	}
	groups := make(map[string]*projectionGroup, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := TimeSeriesProjectionGrainKey(row.GetKey())
		group := groups[grainKey]
		if group == nil {
			group = &projectionGroup{template: row.GetKey(), rowsByDataset: make(map[string]*pb.TimeSeriesRow)}
			groups[grainKey] = group
			order = append(order, grainKey)
		}
		group.rowsByDataset[row.GetKey().GetDatasetId()] = row
	}
	for _, datasetID := range datasetIDs {
		var missing []*pb.TimeSeriesKey
		for _, grainKey := range order {
			group := groups[grainKey]
			if group.rowsByDataset[datasetID] != nil {
				continue
			}
			key := proto.Clone(group.template).(*pb.TimeSeriesKey)
			key.DatasetId = datasetID
			missing = append(missing, key)
		}
		if len(missing) == 0 {
			continue
		}
		if readProjectionRows == nil {
			continue
		}
		for start := 0; start < len(missing); start += projectionReadBatchSize {
			end := min(start+projectionReadBatchSize, len(missing))
			readRows, err := readProjectionRows(ctx, missing[start:end])
			if err != nil {
				return nil, true, err
			}
			for _, read := range readRows {
				if read == nil || read.GetKey() == nil {
					continue
				}
				if read.GetKey().GetDatasetId() != datasetID {
					return nil, true, errors.New("time-series projection batch returned a row from the wrong dataset")
				}
				if group := groups[TimeSeriesProjectionGrainKey(read.GetKey())]; group != nil {
					group.rowsByDataset[datasetID] = read
				}
			}
		}
	}
	out := make([]*pb.TimeSeriesRow, 0, len(order))
	for _, grainKey := range order {
		group := groups[grainKey]
		primaryRow := group.rowsByDataset[primaryDatasetID]
		if primaryRow == nil {
			continue
		}
		out = append(out, &pb.TimeSeriesRow{
			Key:        proto.Clone(primaryRow.GetKey()).(*pb.TimeSeriesKey),
			Columns:    ProjectColumnsForView(primaryDatasetID, columns, group.rowsByDataset),
			Attributes: CloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, true, nil
}

// FilteredTimeSeriesRowsForView applies view.filter_json before projecting
// fact rows into a TimeSeries view.
func FilteredTimeSeriesRowsForView(
	ctx context.Context,
	item *pb.View,
	columns []*pb.ViewColumn,
	rows []*pb.TimeSeriesRow,
	readProjectionRows TimeSeriesProjectionReader,
) ([]*pb.TimeSeriesRow, bool, error) {
	if item == nil || !IsProjectableTimeSeriesView(item, columns) {
		return nil, false, nil
	}
	filtered, err := filterRowsByViewJSON(item, rows)
	if err != nil {
		return nil, true, err
	}
	return TimeSeriesRowsForView(ctx, item, columns, filtered, readProjectionRows)
}

// RecordRowsForView projects record rows into the columns exposed by a view.
func RecordRowsForView(
	ctx context.Context,
	item *pb.View,
	columns []*pb.ViewColumn,
	rows []*pb.RecordRow,
	readProjectionRows RecordProjectionReader,
) ([]*pb.RecordRow, bool, error) {
	if item == nil || !IsProjectableRecordView(item, columns) {
		return nil, false, nil
	}
	primaryDatasetID := item.GetPrimaryDatasetId()
	datasetIDs := ViewProjectionDatasets(primaryDatasetID, columns)
	type projectionGroup struct {
		template      *pb.RecordKey
		rowsByDataset map[string]*pb.RecordRow
	}
	groups := make(map[string]*projectionGroup, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		grainKey := RecordProjectionGrainKey(row.GetKey())
		group := groups[grainKey]
		if group == nil {
			group = &projectionGroup{template: row.GetKey(), rowsByDataset: make(map[string]*pb.RecordRow)}
			groups[grainKey] = group
			order = append(order, grainKey)
		}
		group.rowsByDataset[row.GetKey().GetDatasetId()] = row
	}
	for _, datasetID := range datasetIDs {
		var missing []*pb.RecordKey
		for _, grainKey := range order {
			group := groups[grainKey]
			if group.rowsByDataset[datasetID] != nil {
				continue
			}
			key := proto.Clone(group.template).(*pb.RecordKey)
			key.DatasetId = datasetID
			missing = append(missing, key)
		}
		if len(missing) == 0 || readProjectionRows == nil {
			continue
		}
		for start := 0; start < len(missing); start += projectionReadBatchSize {
			end := min(start+projectionReadBatchSize, len(missing))
			readRows, err := readProjectionRows(ctx, missing[start:end])
			if err != nil {
				return nil, true, err
			}
			for _, read := range readRows {
				if read == nil || read.GetKey() == nil {
					continue
				}
				if read.GetKey().GetDatasetId() != datasetID {
					return nil, true, errors.New("Record projection batch returned a row from the wrong dataset")
				}
				if group := groups[RecordProjectionGrainKey(read.GetKey())]; group != nil {
					group.rowsByDataset[datasetID] = read
				}
			}
		}
	}
	out := make([]*pb.RecordRow, 0, len(order))
	for _, grainKey := range order {
		group := groups[grainKey]
		primaryRow := group.rowsByDataset[primaryDatasetID]
		if primaryRow == nil {
			continue
		}
		out = append(out, &pb.RecordRow{
			Key:        proto.Clone(primaryRow.GetKey()).(*pb.RecordKey),
			Columns:    ProjectRecordColumnsForView(primaryDatasetID, columns, group.rowsByDataset),
			Attributes: CloneStringMap(primaryRow.GetAttributes()),
			Revision:   primaryRow.GetRevision(), UpdatedAt: primaryRow.GetUpdatedAt(), CommitSeq: primaryRow.GetCommitSeq(),
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
		key.GetRecordId(),
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
