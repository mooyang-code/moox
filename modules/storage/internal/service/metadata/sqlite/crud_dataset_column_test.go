package sqlite

import (
	"context"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 列自描述：显示名、分组、状态可以改，类型不能改；列出时按建列顺序。
func TestDatasetColumnsAreSelfDescribing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	createTestDataset(t, ctx, store, "prices")
	for _, name := range []string{"open", "close"} {
		if _, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
			SpaceId: "space", DatasetId: "prices", ColumnName: name,
			ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, DisplayName: name + "价", Group: "行情价格",
		}); err != nil {
			t.Fatal(err)
		}
	}
	columns, _, err := store.ListDatasetColumns(ctx, "space", "prices", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].GetColumnName() != "open" || columns[1].GetColumnName() != "close" {
		t.Fatalf("列应按建列顺序列出：%v", columns)
	}
	if columns[0].GetDisplayName() != "open价" || columns[0].GetGroup() != "行情价格" || columns[0].GetCreatedAt() == "" {
		t.Fatalf("列的显示名、分组、创建时间没有保存：%v", columns[0])
	}

	renamed := &pb.DatasetColumn{
		SpaceId: "space", DatasetId: "prices", ColumnName: "open",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, DisplayName: "开盘价", Group: "行情", Status: "disabled",
	}
	if _, err := store.UpsertDatasetColumn(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDatasetColumn(ctx, "space", "prices", "open")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDisplayName() != "开盘价" || got.GetGroup() != "行情" || got.GetStatus() != "disabled" || got.GetCreatedAt() != columns[0].GetCreatedAt() {
		t.Fatalf("改显示名、分组、状态后：%v", got)
	}

	renamed.ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_INT
	if _, err := store.UpsertDatasetColumn(ctx, renamed); err == nil || !strings.Contains(err.Error(), "类型不能修改") {
		t.Fatalf("改类型应被拒绝：%v", err)
	}
	renamed.ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	renamed.Status = "deleted"
	if _, err := store.UpsertDatasetColumn(ctx, renamed); err == nil {
		t.Fatal("列状态只能是 active 或 disabled")
	}
}
