package pebble

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenInitializesLayoutMarkerForNewAndEmptyPaths(t *testing.T) {
	for _, tt := range []struct {
		name     string
		makePath bool
	}{
		{name: "new path"},
		{name: "empty directory", makePath: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "db")
			if tt.makePath {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			store, err := Open(Options{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(path, layoutMarkerName))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != layoutVersion {
				t.Fatalf("layout marker = %q", data)
			}
		})
	}
}

func TestOpenRejectsNonEmptyStoreWithoutLayoutMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "CURRENT"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Options{Path: path})
	if err == nil || !strings.Contains(err.Error(), "清空 Pebble 库") {
		t.Fatalf("Open() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "CURRENT")); statErr != nil {
		t.Fatalf("legacy file was changed: %v", statErr)
	}
}

func TestOpenRejectsUnsupportedOrDamagedLayoutMarkerWithoutCleanup(t *testing.T) {
	for _, content := range []string{"1\n", "2\n", "3\n", "4\n", "2", "garbage\n"} {
		t.Run(strings.TrimSpace(content), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "db")
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(path, layoutMarkerName)
			if err := os.WriteFile(marker, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(path, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(Options{Path: path}); err == nil {
				t.Fatalf("marker %q accepted", content)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != content {
				t.Fatalf("marker changed: data=%q err=%v", data, err)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
				t.Fatalf("store content changed: data=%q err=%v", data, err)
			}
		})
	}
}

func TestOpenReopensCurrentLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRecoversWhenMarkerLandedBeforePebbleInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, layoutMarkerName), []byte(layoutVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
