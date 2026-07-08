package jobhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintainDailyCreatesFutureTwoDaysAndDeletesPastTwoDays(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(StoreOptions{Dir: dir, RetentionDays: 2})

	for _, name := range []string{"20260704.db", "20260705.db", "20260706.db", "20260707.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	now := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	if err := store.MaintainDaily(ctx, now); err != nil {
		t.Fatalf("MaintainDaily() error = %v", err)
	}

	for _, name := range []string{"20260708.db", "20260709.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("future db %s was not created: %v", name, err)
		}
	}
	for _, name := range []string{"20260704.db", "20260705.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old db %s still exists or stat failed with unexpected error: %v", name, err)
		}
	}
	for _, name := range []string{"20260706.db", "20260707.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("recent db %s should remain: %v", name, err)
		}
	}
}

func TestMaintainDailyUsesTriggerTimezoneDay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(StoreOptions{Dir: dir, RetentionDays: 2})
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	for _, name := range []string{"20260703.db", "20260704.db", "20260705.db", "20260706.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	now := time.Date(2026, 7, 7, 0, 5, 0, 0, loc)
	if err := store.MaintainDaily(ctx, now); err != nil {
		t.Fatalf("MaintainDaily() error = %v", err)
	}

	for _, name := range []string{"20260708.db", "20260709.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("future db %s was not created: %v", name, err)
		}
	}
	for _, name := range []string{"20260705.db", "20260704.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old db %s still exists or stat failed with unexpected error: %v", name, err)
		}
	}
	for _, name := range []string{"20260706.db", "20260703.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("untargeted db %s should remain: %v", name, err)
		}
	}
}
