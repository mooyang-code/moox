package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGenerationCannotRegressAndAdvancesEpoch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:generation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	first, err := AdvanceGeneration(context.Background(), db, "crypto_binance|instrument", now)
	if err != nil || first.Epoch != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	stale, err := AdvanceGeneration(context.Background(), db, "crypto_binance|instrument", now.Add(-time.Hour))
	if err != nil || stale.Epoch != 1 || !stale.Generation.Equal(now) {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	next, err := AdvanceGeneration(context.Background(), db, "crypto_binance|instrument", now.Add(time.Hour))
	if err != nil || next.Epoch != 2 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
