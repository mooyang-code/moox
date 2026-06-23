//go:build !cgo

package duckdb

import (
	"path/filepath"
	"testing"
)

func TestOpenRequiresCGO(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("Open() succeeded without cgo; DuckDB view storage must not fall back to memory")
	}
	if got := err.Error(); got != errDuckDBRequiresCGO.Error() {
		t.Fatalf("Open() error = %q, want %q", got, errDuckDBRequiresCGO.Error())
	}
}
