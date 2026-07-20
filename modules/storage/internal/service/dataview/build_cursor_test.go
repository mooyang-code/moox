//go:build legacy_storage

package view

import "testing"

func TestBuildCursorRoundTrip(t *testing.T) {
	want := buildCursor{Phase: buildPhaseCatchUp, Cursor: "next-page"}
	raw, err := encodeBuildCursor(want)
	if err != nil {
		t.Fatalf("encodeBuildCursor: %v", err)
	}
	got, err := decodeBuildCursor(raw)
	if err != nil {
		t.Fatalf("decodeBuildCursor: %v", err)
	}
	if got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestEmptyBuildCursorStartsBackfill(t *testing.T) {
	got, err := decodeBuildCursor("")
	if err != nil {
		t.Fatalf("decodeBuildCursor: %v", err)
	}
	if got.Phase != buildPhaseBackfill || got.Cursor != "" {
		t.Fatalf("cursor = %+v", got)
	}
}
