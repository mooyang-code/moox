package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestTightenPrivate(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := TightenPrivate(open); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(open); info.Mode().Perm() != 0o700 {
		t.Fatalf("755 应收紧到 700: %v", info.Mode())
	}
	writable := filepath.Join(base, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := TightenPrivate(writable); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(writable); info.Mode().Perm() != 0o775 {
		t.Fatalf("别人可写的目录不能悄悄收紧: %v", info.Mode())
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(open, link); err != nil {
		t.Fatal(err)
	}
	if err := TightenPrivate(link); err != nil {
		t.Fatal(err)
	}
	_, err = OpenPhysicalRoot(writable, true)
	if err == nil || !strings.Contains(err.Error(), writable) {
		t.Fatalf("报错要带路径: %v", err)
	}
}
