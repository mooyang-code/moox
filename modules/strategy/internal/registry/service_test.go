package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

const validManifest = "api_version: moox.strategy/v1\nentrypoint: strategy.py:run\ninput:\n  history_bars: 200\n"

func TestParseManifestRequiresAPIVersionEntrypointAndInputWindow(t *testing.T) {
	got, err := Parse(validManifest)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != "moox.strategy/v1" || got.Entrypoint != "strategy.py:run" || got.Input.HistoryBars != 200 {
		t.Fatalf("Parse() = %+v", got)
	}
	for _, raw := range []string{
		"entrypoint: strategy.py:run\ninput:\n  history_bars: 200\n",
		"api_version: moox.strategy/v1\ninput:\n  history_bars: 200\n",
		"api_version: moox.strategy/v1\nentrypoint: strategy.py:run\n",
		"api_version: moox.strategy/v1\nentrypoint: strategy.py:run\ninput:\n  history_bars: 0\n",
	} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("invalid manifest accepted:\n%s", raw)
		}
	}
}

func TestParseManifestRejectsStateFields(t *testing.T) {
	for _, field := range []string{"state", "next_state", "state_schema", "state_schema_version", "state_format_version"} {
		raw := validManifest + field + ": {}\n"
		if _, err := Parse(raw); err == nil {
			t.Fatalf("state field %q was accepted", field)
		}
	}
}

func TestParseManifestRejectsUnsupportedAPIVersion(t *testing.T) {
	if _, err := Parse("api_version: moox.strategy/v2\nentrypoint: strategy.py:run\ninput:\n  history_bars: 200\n"); err == nil {
		t.Fatal("unsupported api_version was accepted")
	}
}

func TestSaveStrategyRejectsChangedArtifactWithSameID(t *testing.T) {
	repo := openRegistryStore(t)
	service := &Service{Repo: repo}
	first, err := service.Prepare("strategy-1", "trend", validManifest, "def run(context, data, params): return {'action':'hold'}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	changed, err := service.Prepare("strategy-1", "trend", validManifest, "def run(context, data, params): return {'action':'rebalance','targets':[]}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Save(context.Background(), changed); !errors.Is(err, ErrImmutableStrategy) {
		t.Fatalf("Save() error = %v", err)
	}
}

func TestSaveStrategyAcceptsChangedSourceWithNewID(t *testing.T) {
	repo := openRegistryStore(t)
	service := &Service{Repo: repo}
	for _, item := range []struct {
		id, source string
	}{
		{"strategy-1", "def run(context, data, params): return {'action':'hold'}\n"},
		{"strategy-2", "def run(context, data, params): return {'action':'rebalance','targets':[]}\n"},
	} {
		strategy, err := service.Prepare(item.id, "trend", validManifest, item.source)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Save(context.Background(), strategy); err != nil {
			t.Fatal(err)
		}
	}
}

func openRegistryStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	return store.New(db)
}

func TestPrepareReturnsImmutableStrategyShape(t *testing.T) {
	strategy, err := (&Service{}).Prepare("strategy-1", "trend", validManifest, "def run(context, data, params): return {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if strategy.ID != "strategy-1" || strategy.Name != "trend" || strategy.SourceHash == "" {
		t.Fatalf("Prepare() = %+v", strategy)
	}
	_ = domain.Strategy(strategy)
}
