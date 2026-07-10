package viewindex

import (
	"path/filepath"
	"testing"
)

func TestViewIndexIDRoundTrip(t *testing.T) {
	id := ViewIndexID("crypto", "binance_spot_kline", SlotB)
	ref, err := ParseViewIndexID(id)
	if err != nil {
		t.Fatalf("ParseViewIndexID() error = %v", err)
	}
	want := Ref{SpaceID: "crypto", ViewID: "binance_spot_kline", Slot: SlotB}
	if ref != want {
		t.Fatalf("ParseViewIndexID() = %+v, want %+v", ref, want)
	}
	if ref.ID() != id {
		t.Fatalf("Ref.ID() = %q, want %q", ref.ID(), id)
	}
}

func TestPhysicalPathsUseEncodedViewDirectories(t *testing.T) {
	root := t.TempDir()
	ref := Ref{SpaceID: "crypto", ViewID: "binance_spot_kline", Slot: SlotB}
	wantDuckDB := filepath.Join(root, "duckdb", "63727970746f", "62696e616e63655f73706f745f6b6c696e65", "b.duckdb")
	if got := DuckDBPath(root, ref); got != wantDuckDB {
		t.Fatalf("DuckDBPath() = %q, want %q", got, wantDuckDB)
	}
	wantBleve := filepath.Join(root, "bleve", "63727970746f", "62696e616e63655f73706f745f6b6c696e65", "b")
	if got := BlevePath(root, ref); got != wantBleve {
		t.Fatalf("BlevePath() = %q, want %q", got, wantBleve)
	}
}

func TestParseViewIndexIDRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		"",
		"../a",
		"view_szz_v01_a",
		"view_s63727970746f_v73706f74_c",
		"view_s00_v73706f74_a",
	} {
		if _, err := ParseViewIndexID(value); err == nil {
			t.Fatalf("ParseViewIndexID(%q) error = nil", value)
		}
	}
}

func TestInactiveViewIndexIDUsesOppositeSlot(t *testing.T) {
	a := ViewIndexID("crypto", "spot", SlotA)
	b := ViewIndexID("crypto", "spot", SlotB)
	if got := InactiveViewIndexID("crypto", "spot", a); got != b {
		t.Fatalf("InactiveViewIndexID(a) = %q, want %q", got, b)
	}
	if got := InactiveViewIndexID("crypto", "spot", b); got != a {
		t.Fatalf("InactiveViewIndexID(b) = %q, want %q", got, a)
	}
	if got := InactiveViewIndexID("crypto", "spot", "not-an-index"); got != a {
		t.Fatalf("InactiveViewIndexID(invalid) = %q, want %q", got, a)
	}
}
