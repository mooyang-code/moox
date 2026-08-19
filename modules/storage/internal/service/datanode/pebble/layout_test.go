package pebble

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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
			store, err := Open(Options{Path: path, NodeID: "node-1"})
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
			if string(data) != "4\n" {
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
	_, err := Open(Options{Path: path, NodeID: "node-1"})
	if err == nil || !strings.Contains(err.Error(), "reset DataNode store") {
		t.Fatalf("Open() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "CURRENT")); statErr != nil {
		t.Fatalf("legacy file was changed: %v", statErr)
	}
}

func TestOpenRejectsUnsupportedOrDamagedLayoutMarkerWithoutCleanup(t *testing.T) {
	for _, content := range []string{"1\n", "2", "garbage\n"} {
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
			if _, err := Open(Options{Path: path, NodeID: "node-1"}); err == nil {
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

func TestEnsureLayoutUpgradesVersionTwoWithoutDeletingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, layoutMarkerName)
	if err := os.WriteFile(marker, []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "CURRENT")
	if err := os.WriteFile(sentinel, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureLayout(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != layoutVersion {
		t.Fatalf("layout marker = %q, %v", data, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "legacy" {
		t.Fatalf("legacy data changed: data=%q err=%v", data, err)
	}
}

func TestOpenReopensVersionThreeLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := Open(Options{Path: path, NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Options{Path: path, NodeID: "node-1"})
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
	if err := createLayoutMarker(path, filepath.Join(path, layoutMarkerName)); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Path: path, NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureLayoutRecoversStrictlyNamedTemporaryMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(path, layoutMarkerTempPrefix+"stale")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureLayout(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("temporary marker still exists: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(path, layoutMarkerName))
	if err != nil || string(data) != layoutVersion {
		t.Fatalf("layout marker = %q, %v", data, err)
	}
}

func TestEnsureLayoutDoesNotIgnoreSimilarTemporaryNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "."+layoutMarkerName+".tmp")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureLayout(path)
	if err == nil || !strings.Contains(err.Error(), "reset DataNode store") {
		t.Fatalf("ensureLayout() error = %v", err)
	}
	if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "keep" {
		t.Fatalf("similar file was changed: data=%q err=%v", data, readErr)
	}
}

func TestEnsureLayoutConcurrentInitializationConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			errs <- ensureLayout(path)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ensureLayout() = %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(path, layoutMarkerName))
	if err != nil || string(data) != layoutVersion {
		t.Fatalf("layout marker = %q, %v", data, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), layoutMarkerTempPrefix) {
			t.Fatalf("temporary marker remains: %s", entry.Name())
		}
	}
}
