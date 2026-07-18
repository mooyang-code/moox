package builder

import (
	"github.com/mooyang-code/moox/modules/storage/internal/service/view"
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
		result = append(result, fragment)
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
		fragment := &pb.TimeSeriesRow{Key: proto.Clone(row.GetKey()).(*pb.TimeSeriesKey)}
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == row.GetKey().GetDatasetId() {
				fragment.RemovedColumnNames = append(fragment.RemovedColumnNames, column.GetColumnName())
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
		fragment := &pb.RecordRow{Key: proto.Clone(row.GetKey()).(*pb.RecordKey)}
		fragment.Key.DatasetId = item.GetPrimaryDatasetId()
		for _, column := range columns {
			if column != nil && view.ViewColumnOriginDataset(item.GetPrimaryDatasetId(), column) == row.GetKey().GetDatasetId() {
				fragment.RemovedColumnNames = append(fragment.RemovedColumnNames, column.GetColumnName())
			}
		}
		partial = append(partial, fragment)
	}
	return partial, full
}
