package domain

import (
	"strings"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestScalarFromColumnPreservesZeroValues(t *testing.T) {
	zero := int64(0)
	falseValue := false
	cases := []*storagepb.ColumnValue{
		{ColumnName: "trade_num", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: zero}}},
		{ColumnName: "closed", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: falseValue}}},
	}
	for _, column := range cases {
		scalar, err := ScalarFromColumn(column)
		if err != nil || scalar.PointerCount() != 1 {
			t.Fatalf("ScalarFromColumn(%s) = %#v, %v", column.GetColumnName(), scalar, err)
		}
	}
}

func TestScalarFromColumnRejectsTypeBranchMismatch(t *testing.T) {
	column := &storagepb.ColumnValue{ColumnName: "close", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "1.25"}}}
	if _, err := ScalarFromColumn(column); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("ScalarFromColumn() error = %v", err)
	}
}
