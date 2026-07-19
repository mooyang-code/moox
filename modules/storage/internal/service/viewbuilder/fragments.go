//go:build legacy_storage

package builder

import (
	"github.com/mooyang-code/moox/modules/storage/internal/service/dataview"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// timeSeriesFragments keeps only the columns owned by the dataset in the
// committed event. Missing source datasets must not become delete patches.
func timeSeriesFragments(item *pb.View, columns []*pb.ViewColumn, sourceRows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	result := make([]*pb.TimeSeriesRow, 0, len(sourceRows))
	for _, row := range sourceRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		source := row
		fragment := proto.Clone(row).(*pb.TimeSeriesRow)
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		fragment.Columns = nil
		if source.GetKey().GetDatasetId() != item.GetPrimaryDatasetId() {
			fragment.Attributes = nil
			fragment.AttributesToDelete = nil
		}
		for _, value := range source.GetColumns() {
			for _, column := range columns {
				if column == nil || view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) != source.GetKey().GetDatasetId() || view.ViewColumnSourceName(source.GetKey().GetDatasetId(), column) != value.GetColumnName() {
					continue
				}
				mappedColumn := proto.Clone(value).(*pb.ColumnValue)
				mappedColumn.ColumnName = column.GetColumnName()
				if mappedColumn.GetValueType() == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
					mappedColumn.ValueType = column.GetValueType()
				}
				fragment.Columns = append(fragment.Columns, mappedColumn)
			}
		}
		fragment.RemovedColumnNames = mappedRemovedColumnNames(item, columns, source.GetKey().GetDatasetId(), source.GetRemovedColumnNames())
		fragment.RemovedColumns = mappedRemovedColumns(item, columns, source.GetKey().GetDatasetId(), source.GetRemovedColumnNames(), source.GetRemovedColumns(), source.GetSourceShardId(), source.GetSourceSequence())
		result = append(result, fragment)
	}
	return result
}

func recordFragments(item *pb.View, columns []*pb.ViewColumn, sourceRows []*pb.RecordRow) []*pb.RecordRow {
	result := make([]*pb.RecordRow, 0, len(sourceRows))
	for _, row := range sourceRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		source := row
		fragment := proto.Clone(row).(*pb.RecordRow)
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		fragment.Columns = nil
		if source.GetKey().GetDatasetId() != item.GetPrimaryDatasetId() {
			fragment.Attributes = nil
			fragment.AttributesToDelete = nil
		}
		for _, value := range source.GetColumns() {
			for _, column := range columns {
				if column == nil || view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) != source.GetKey().GetDatasetId() || view.ViewColumnSourceName(source.GetKey().GetDatasetId(), column) != value.GetColumnName() {
					continue
				}
				mappedColumn := proto.Clone(value).(*pb.ColumnValue)
				mappedColumn.ColumnName = column.GetColumnName()
				if mappedColumn.GetValueType() == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
					mappedColumn.ValueType = column.GetValueType()
				}
				fragment.Columns = append(fragment.Columns, mappedColumn)
			}
		}
		fragment.RemovedColumnNames = mappedRemovedColumnNames(item, columns, source.GetKey().GetDatasetId(), source.GetRemovedColumnNames())
		fragment.RemovedColumns = mappedRemovedColumns(item, columns, source.GetKey().GetDatasetId(), source.GetRemovedColumnNames(), source.GetRemovedColumns(), source.GetSourceShardId(), source.GetSourceSequence())
		result = append(result, fragment)
	}
	return result
}

func mappedRemovedColumnNames(item *pb.View, columns []*pb.ViewColumn, sourceDataset string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == sourceDataset && view.ViewColumnSourceName(sourceDataset, column) == name {
				result = append(result, column.GetColumnName())
			}
		}
	}
	return result
}

func mappedRemovedColumns(item *pb.View, columns []*pb.ViewColumn, sourceDataset string, names []string, removals []*pb.ColumnRemoval, sourceShardID string, sourceSequence uint64) []*pb.ColumnRemoval {
	result := make([]*pb.ColumnRemoval, 0, len(removals)+len(names))
	for _, name := range names {
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == sourceDataset && view.ViewColumnSourceName(sourceDataset, column) == name {
				result = append(result, &pb.ColumnRemoval{ColumnName: column.GetColumnName(), SourceShardId: sourceShardID, SourceSequence: sourceSequence})
			}
		}
	}
	for _, removal := range removals {
		if removal == nil {
			continue
		}
		for _, column := range columns {
			if column == nil || view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) != sourceDataset || view.ViewColumnSourceName(sourceDataset, column) != removal.GetColumnName() {
				continue
			}
			mapped := proto.Clone(removal).(*pb.ColumnRemoval)
			mapped.ColumnName = column.GetColumnName()
			result = append(result, mapped)
		}
	}
	return result
}

func splitTimeSeriesDeletes(item *pb.View, columns []*pb.ViewColumn, rows []*pb.TimeSeriesRow) (partial, full []*pb.TimeSeriesRow) {
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		if row.GetKey().GetDatasetId() == item.GetPrimaryDatasetId() {
			full = append(full, row)
			continue
		}
		fragment := &pb.TimeSeriesRow{Key: proto.Clone(row.GetKey()).(*pb.TimeSeriesKey), SourceShardId: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()}
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == row.GetKey().GetDatasetId() {
				fragment.RemovedColumnNames = append(fragment.RemovedColumnNames, column.GetColumnName())
				fragment.RemovedColumns = append(fragment.RemovedColumns, &pb.ColumnRemoval{ColumnName: column.GetColumnName(), SourceShardId: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
			}
		}
		partial = append(partial, fragment)
	}
	return partial, full
}

func splitRecordDeletes(item *pb.View, columns []*pb.ViewColumn, rows []*pb.RecordRow) (partial, full []*pb.RecordRow) {
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		if row.GetKey().GetDatasetId() == item.GetPrimaryDatasetId() {
			full = append(full, row)
			continue
		}
		fragment := &pb.RecordRow{Key: proto.Clone(row.GetKey()).(*pb.RecordKey), SourceShardId: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()}
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == row.GetKey().GetDatasetId() {
				fragment.RemovedColumnNames = append(fragment.RemovedColumnNames, column.GetColumnName())
				fragment.RemovedColumns = append(fragment.RemovedColumns, &pb.ColumnRemoval{ColumnName: column.GetColumnName(), SourceShardId: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
			}
		}
		partial = append(partial, fragment)
	}
	return partial, full
}
