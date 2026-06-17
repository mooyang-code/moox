package testutil

import pb "github.com/mooyang-code/moox/modules/storage/proto/gen"

func StringValue(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func DoubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func IntValue(name string, value int64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}},
	}
}
