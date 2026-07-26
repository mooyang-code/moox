package rowidentity

import (
	"testing"
)

func TestCanonicalDimensionsIsOrderIndependent(t *testing.T) {
	first, err := CanonicalDimensions(map[string]string{"mountpoint": "/", "device": "/dev/disk1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalDimensions(map[string]string{"device": "/dev/disk1", "mountpoint": "/"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != `{"device":"/dev/disk1","mountpoint":"/"}` {
		t.Fatalf("canonical dimensions differ: %q %q", first, second)
	}
}

func TestCanonicalDimensionsRejectsEmptyName(t *testing.T) {
	if _, err := CanonicalDimensions(map[string]string{"": "value"}); err == nil {
		t.Fatal("empty dimension name was accepted")
	}
}
