package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func openCurrentStore(t *testing.T) *Store {
	t.Helper()
	repo, err := Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestStrategyStoreRoundTrip(t *testing.T) {
	repo := openCurrentStore(t)
	want := domain.Strategy{
		ID:           "strategy-1",
		Name:         "trend",
		Kind:         "coin_selection",
		ManifestYAML: "api_version: moox.strategy/v2",
		CompiledJSON: []byte(`{"api_version":"moox.strategy/v2","kind":"coin_selection"}`),
		SourceHash:   "sha256",
		CreatedAt:    time.UnixMilli(1000).UTC(),
	}
	if err := repo.SaveStrategy(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStrategy(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Name != want.Name || got.Kind != want.Kind || string(got.CompiledJSON) != string(want.CompiledJSON) || got.SourceHash != want.SourceHash || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("GetStrategy() = %+v, want %+v", got, want)
	}
}
