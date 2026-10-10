package fsutil

import "testing"

func TestUnitRootsPermitNestedLayoutsAndRejectStateOrUnitOverlap(t *testing.T) {
	if err := ValidateUnitRoots("/data/moox", "/data/moox/host", "/data/moox/units/prod"); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"/data/moox", "/data/other", "/data/moox/../other", "/data/moox/host", "/data/moox/host/prod", "/data/moox/bootstrap", "/data/moox/bootstrap/units/prod", "/data/moox/bootstrap-input/prod", "/data/moox/identity/prod", "/data/moox/run/prod", "relative"} {
		if err := ValidateUnitRoots("/data/moox", "/data/moox/host", unit); err == nil {
			t.Fatalf("accepted unsafe unit root %q", unit)
		}
	}
	if err := ValidateUnitRoots("/data/moox", "/data/moox/units/host", "/data/moox/units"); err == nil {
		t.Fatal("accepted a unit containing another unit")
	}
}
