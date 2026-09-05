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

func TestLegacySaveStrategyWritesDSLNameColumns(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	want := domain.Strategy{ID: "s1", Name: "trend", ManifestYAML: "name: trend", CreatedAt: now}
	if err := repo.SaveStrategy(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	var row struct {
		StrategyName string
		DSLYaml      string
	}
	if err := repo.db.Table("t_strategies").Where("strategy_id = ?", want.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.StrategyName != want.Name || row.DSLYaml != want.ManifestYAML {
		t.Fatalf("stored strategy = %+v", row)
	}
}
