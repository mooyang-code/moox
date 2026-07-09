package viewindex

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestViewIndexIDAlternatesBetweenSlots(t *testing.T) {
	slotA := ViewIndexID("crypto", "spot_kline_1m_view", "a")
	if slotA != "view_crypto_spot_kline_1m_view_a" {
		t.Fatalf("slot a = %q", slotA)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", slotA); got != "view_crypto_spot_kline_1m_view_b" {
		t.Fatalf("inactive slot = %q", got)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", ""); got != slotA {
		t.Fatalf("empty active inactive slot = %q", got)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", "view_crypto_legacy_123"); got != slotA {
		t.Fatalf("non-slot active inactive slot = %q", got)
	}
}

func TestViewIndexSchemaHashIgnoresViewVersion(t *testing.T) {
	base := ViewIndexSchema{
		SpaceID:     "crypto",
		ViewID:      "spot",
		ViewVersion: 1,
		Engine:      "duckdb",
		Columns: []*pb.ViewColumn{{
			ColumnName: "close",
			OriginId:   "binance_spot_kline.close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}},
	}
	bumped := base
	bumped.ViewVersion = 2
	if HashViewIndexSchema(base) != HashViewIndexSchema(bumped) {
		t.Fatal("schema hash changed after version-only bump")
	}
	withVolume := base
	withVolume.Columns = append(withVolume.Columns, &pb.ViewColumn{
		ColumnName: "volume",
		OriginId:   "binance_spot_kline.volume",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	if HashViewIndexSchema(base) == HashViewIndexSchema(withVolume) {
		t.Fatal("schema hash did not change after column add")
	}
}

func TestBuildingIndexWritableRequiresBuildingStatusAndCurrentVersion(t *testing.T) {
	view := &pb.View{
		ViewVersion:         3,
		BuildingViewVersion: 3,
		BuildingResult:      "view_crypto_spot_b",
		BuildStatus:         "failed",
	}
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable for failed status, want false")
	}
	view.BuildStatus = "building"
	if !BuildingIndexWritable(view) {
		t.Fatal("building index not writable for current building version, want true")
	}
	view.BuildingViewVersion = 2
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable for stale version, want false")
	}
	view.BuildingViewVersion = 3
	view.BuildingResult = ""
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable without building result, want false")
	}
}
