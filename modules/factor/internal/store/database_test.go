package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerInitializeAndClose(t *testing.T) {
	mgr := NewManager()
	if err := mgr.Initialize(&Options{Path: filepath.Join(t.TempDir(), "factor.db")}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if mgr.DB() == nil {
		t.Fatal("DB() returned nil")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestBuildSQLiteDSNUsesDurablePragmas(t *testing.T) {
	dsn := buildSQLiteDSN("./data/factor/factor.db")

	for _, want := range []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("dsn %q does not contain %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "synchronous(OFF)") {
		t.Fatalf("dsn %q must not disable SQLite synchronization", dsn)
	}
}
