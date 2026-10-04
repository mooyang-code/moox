package registry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestPrepareCompiledAndSaveAreImmutable(t *testing.T) {
	repo := openRegistryStore(t)
	service := &Service{Repo: repo, Now: func() time.Time { return time.UnixMilli(1000) }}
	compiled := compiler.CompiledStrategy{APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space-1", CompiledJSON: []byte(`{"api_version":"moox.strategy/v2","kind":"coin_selection","space_id":"space-1"}`)}
	first, err := service.PrepareCompiled("strategy-1", "trend", "manifest-a", compiled)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetStrategy(context.Background(), first.ID); err != nil || got.SourceHash != first.SourceHash {
		t.Fatalf("stored strategy = %+v, err=%v", got, err)
	}
	changed := first
	changed.ManifestYAML = "manifest-b"
	changed.SourceHash = "different"
	if err := service.Save(context.Background(), changed); !errors.Is(err, ErrImmutableStrategy) {
		t.Fatalf("Save changed artifact error = %v", err)
	}
}

func openRegistryStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	repo := store.New(db)
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}
