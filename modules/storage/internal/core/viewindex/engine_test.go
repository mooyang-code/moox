package viewindex

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestViewIndexIDAlternatesBetweenSlots(t *testing.T) {
	slotA := ViewIndexID("crypto", "spot_kline_1m_view", "a")
	slotB := ViewIndexID("crypto", "spot_kline_1m_view", "b")
	if slotA == "" || slotB == "" || slotA == slotB {
		t.Fatalf("slots = %q/%q, want two stable non-empty names", slotA, slotB)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", slotA); got != slotB {
		t.Fatalf("inactive slot = %q", got)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", ""); got != slotA {
		t.Fatalf("empty active inactive slot = %q", got)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", "view_crypto_legacy_123"); got != slotA {
		t.Fatalf("non-slot active inactive slot = %q", got)
	}
}

func TestViewIndexIDDoesNotCollideWhenPartsContainUnderscores(t *testing.T) {
	left := ViewIndexID("a_b", "c", "a")
	right := ViewIndexID("a", "b_c", "a")
	if left == right {
		t.Fatalf("ViewIndexID collision: %q", left)
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

func TestBuildIndexWritableRequiresWritableStateAndCurrentVersion(t *testing.T) {
	columns := []*pb.ViewColumn{{
		ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId: "market.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}
	view := &pb.View{
		SpaceId: "crypto", ViewId: "spot", Engine: "duckdb", ViewVersion: 3, Columns: columns,
		IndexBuild: &pb.ViewIndexBuild{
			TargetViewVersion: 3,
			IndexId:           ViewIndexID("crypto", "spot", SlotB),
			Engine:            "duckdb",
			Columns:           columns,
			State:             pb.ViewIndexBuild_FAILED,
		},
	}
	view.IndexBuild.SchemaHash = HashViewIndexSchema(ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), Engine: view.GetEngine(), Columns: columns})
	if BuildIndexWritable(view) {
		t.Fatal("building index writable for failed status, want false")
	}
	view.IndexBuild.State = pb.ViewIndexBuild_BUILDING
	if !BuildIndexWritable(view) {
		t.Fatal("building index not writable for current building version, want true")
	}
	view.IndexBuild.State = pb.ViewIndexBuild_CATCHING_UP
	if !BuildIndexWritable(view) {
		t.Fatal("catching-up index not writable, want true")
	}
	view.IndexBuild.TargetViewVersion = 2
	if BuildIndexWritable(view) {
		t.Fatal("building index writable for stale version, want false")
	}
	view.IndexBuild.TargetViewVersion = 3
	view.IndexBuild.IndexId = ""
	if BuildIndexWritable(view) {
		t.Fatal("building index writable without building result, want false")
	}
}

func TestWritableIndexIDsAppliesStateVersionSchemaAndDedupGuards(t *testing.T) {
	columns := []*pb.ViewColumn{{
		ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId: "market.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}
	activeID := ViewIndexID("crypto", "spot", SlotA)
	buildID := ViewIndexID("crypto", "spot", SlotB)
	schemaHash := HashViewIndexSchema(ViewIndexSchema{SpaceID: "crypto", ViewID: "spot", Engine: "duckdb", Columns: columns})
	view := &pb.View{
		SpaceId: "crypto", ViewId: "spot", Engine: "duckdb", ViewVersion: 2,
		ActiveIndexId: activeID, Columns: columns,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: buildID, Engine: "duckdb", TargetViewVersion: 2,
			State: pb.ViewIndexBuild_BUILDING, SchemaHash: schemaHash, Columns: columns,
		},
	}
	assertIndexIDs(t, WritableIndexIDs(view), activeID, buildID)

	view.IndexBuild.State = pb.ViewIndexBuild_CATCHING_UP
	assertIndexIDs(t, WritableIndexIDs(view), activeID, buildID)

	view.IndexBuild.State = pb.ViewIndexBuild_PREPARING
	assertIndexIDs(t, WritableIndexIDs(view), activeID)
	view.IndexBuild.State = pb.ViewIndexBuild_FAILED
	assertIndexIDs(t, WritableIndexIDs(view), activeID)

	view.IndexBuild.State = pb.ViewIndexBuild_BUILDING
	view.IndexBuild.SchemaHash = "stale"
	assertIndexIDs(t, WritableIndexIDs(view), activeID)

	view.IndexBuild.SchemaHash = schemaHash
	view.IndexBuild.IndexId = activeID
	assertIndexIDs(t, WritableIndexIDs(view), activeID)
}

func assertIndexIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("WritableIndexIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WritableIndexIDs = %v, want %v", got, want)
		}
	}
}

func TestViewIndexProtocolUsesIndexIdentity(t *testing.T) {
	item := &pb.View{
		ActiveIndexId: "view_s63727970746f_v73706f74_a",
		IndexBuild: &pb.ViewIndexBuild{
			BuildId: "build-1",
			IndexId: "view_s63727970746f_v73706f74_b",
			State:   pb.ViewIndexBuild_PREPARING,
		},
	}
	req := &pb.PrepareViewIndexReq{
		IndexId: item.GetIndexBuild().GetIndexId(),
		Engine:  "duckdb",
	}
	if item.GetActiveIndexId() == "" || req.GetIndexId() == "" {
		t.Fatal("new View index protocol fields must be populated")
	}
	stats := &pb.ViewIndexStats{FreeDiskBytes: 1024}
	if stats.GetFreeDiskBytes() != 1024 {
		t.Fatal("ViewIndexStats must expose owner free disk bytes")
	}
}
