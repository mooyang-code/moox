package builder

import (
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func builderTestTSRow(spaceID, datasetID, subjectID, dataTime string, columns ...*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			SubjectId: subjectID,
			Freq:      "1m",
			DataTime:  dataTime,
		},
		Columns: columns,
	}
}

func builderTestRecordRow(spaceID, datasetID, recordID, version string, columns ...*pb.ColumnValue) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			RecordId:  recordID,
			Version:   version,
		},
		Columns: columns,
	}
}

func builderTestValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func builderColumnDouble(row *pb.TimeSeriesRow, name string) float64 {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue().GetDoubleValue()
		}
	}
	return 0
}

func builderRecordColumnDouble(row *pb.RecordRow, name string) float64 {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue().GetDoubleValue()
		}
	}
	return 0
}

