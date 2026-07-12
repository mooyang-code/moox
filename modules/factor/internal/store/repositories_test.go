package store

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"gorm.io/gorm"
)

func TestFactorRepositoryUpsertAndListEnabledTimeseries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewFactorRepository(db)

	first := domain.FactorDef{
		FactorID:      "bias",
		Name:          "Bias",
		Kind:          domain.FactorKindTimeseries,
		SourceCode:    "def signal(): pass",
		SourceHash:    "hash-a",
		ParamsJSON:    "[20]",
		LookbackBars:  200,
		WritebackBars: 5,
		DependsJSON:   "[]",
		Status:        domain.FactorStatusEnabled,
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}
	second := first
	second.SourceCode = "def signal(): return 1"
	second.SourceHash = "hash-b"
	second.ParamsJSON = "[20,96]"
	if err := repo.Upsert(ctx, second); err != nil {
		t.Fatalf("Upsert(second) error = %v", err)
	}

	got, err := repo.Get(ctx, "bias")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.SourceHash != "hash-b" || got.ParamsJSON != "[20,96]" {
		t.Fatalf("factor not updated: %+v", got)
	}

	if err := repo.Upsert(ctx, domain.FactorDef{
		FactorID:      "rank",
		Name:          "RankPct",
		Kind:          domain.FactorKindCrossSection,
		SourceCode:    "x",
		SourceHash:    "hash-c",
		ParamsJSON:    "[1608]",
		LookbackBars:  4824,
		WritebackBars: 5,
		DependsJSON:   "[]",
		Status:        domain.FactorStatusEnabled,
	}); err != nil {
		t.Fatalf("Upsert(cross-section) error = %v", err)
	}
	enabled, err := repo.ListEnabledTimeseries(ctx)
	if err != nil {
		t.Fatalf("ListEnabledTimeseries() error = %v", err)
	}
	if len(enabled) != 1 || enabled[0].FactorID != "bias" {
		t.Fatalf("enabled timeseries = %+v", enabled)
	}
}

func TestFactorRepositoryRejectsDuplicateName(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewFactorRepository(db)
	first := domain.FactorDef{FactorID: "factor-a", Name: "SameName", Kind: domain.FactorKindTimeseries, SourceCode: "x", SourceHash: "hash-a", ParamsJSON: "[20]", LookbackBars: 200, WritebackBars: 5, DependsJSON: "[]", Status: domain.FactorStatusEnabled}
	second := first
	second.FactorID = "factor-b"
	second.SourceHash = "hash-b"
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, second); err == nil {
		t.Fatal("expected duplicate factor name error")
	}
}

func TestBindingRepositoryUpsertByNaturalKeyAndFilterBySource(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedFactor(t, db, "bias")
	repo := NewBindingRepository(db)

	binding := domain.FactorBinding{
		BindingID:     "bind-a",
		FactorID:      "bias",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   domain.SubjectModeInclude,
		SubjectsJSON:  `["BTC-USDT","ETH-USDT"]`,
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}
	if err := repo.Upsert(ctx, binding); err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}
	binding.BindingID = "bind-b"
	binding.SubjectsJSON = `["BTC-USDT"]`
	if err := repo.Upsert(ctx, binding); err != nil {
		t.Fatalf("Upsert(second) error = %v", err)
	}

	var count int64
	if err := db.Model(&domain.FactorBinding{}).Count(&count).Error; err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("binding count = %d, want 1", count)
	}

	rows, err := repo.ListEnabledBySource(ctx, "crypto", "binance_spot_kline", "1m")
	if err != nil {
		t.Fatalf("ListEnabledBySource() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d", len(rows))
	}
	if rows[0].BindingID != "bind-b" || rows[0].SubjectMode != domain.SubjectModeInclude || rows[0].SubjectsJSON != `["BTC-USDT"]` {
		t.Fatalf("binding not updated/round-tripped: %+v", rows[0])
	}
}

func TestBindingRepositoryUpsertByBindingIDCanChangeNaturalKey(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedFactor(t, db, "bias")
	seedFactor(t, db, "cci")
	repo := NewBindingRepository(db)

	binding := domain.FactorBinding{
		BindingID:     "bind-a",
		FactorID:      "bias",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   domain.SubjectModeAll,
		SubjectsJSON:  "[]",
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}
	if err := repo.Upsert(ctx, binding); err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}
	binding.FactorID = "cci"
	binding.SourceDataset = "binance_swap_kline"
	binding.Freq = "5m"
	binding.TargetDataset = "binance_swap_factor"
	if err := repo.Upsert(ctx, binding); err != nil {
		t.Fatalf("Upsert(update natural key) error = %v", err)
	}

	var count int64
	if err := db.Model(&domain.FactorBinding{}).Count(&count).Error; err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("binding count = %d, want 1", count)
	}
	rows, err := repo.ListEnabledBySource(ctx, "crypto", "binance_swap_kline", "5m")
	if err != nil {
		t.Fatalf("ListEnabledBySource() error = %v", err)
	}
	if len(rows) != 1 || rows[0].BindingID != "bind-a" || rows[0].FactorID != "cci" {
		t.Fatalf("binding not updated by id: %+v", rows)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(factorschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func seedFactor(t *testing.T, db *gorm.DB, factorID string) {
	t.Helper()

	factor := domain.FactorDef{
		FactorID:      factorID,
		Name:          "Factor-" + factorID,
		Kind:          domain.FactorKindTimeseries,
		SourceCode:    "x",
		SourceHash:    "hash",
		ParamsJSON:    "[20]",
		LookbackBars:  200,
		WritebackBars: 5,
		DependsJSON:   "[]",
		Status:        domain.FactorStatusEnabled,
	}
	if err := db.Create(&factor).Error; err != nil {
		t.Fatalf("seed factor: %v", err)
	}
}
