package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLeaderLeaseFencesStandbyAndIncrementsOnTakeover(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:leader?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	first, err := AcquireLeader(context.Background(), db, "collector", "a", now, time.Minute)
	if err != nil || !first.Acquired || first.Epoch != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	standby, err := AcquireLeader(context.Background(), db, "collector", "b", now.Add(time.Second), time.Minute)
	if err != nil || standby.Acquired || standby.OwnerID != "a" {
		t.Fatalf("standby=%+v err=%v", standby, err)
	}
	takeover, err := AcquireLeader(context.Background(), db, "collector", "b", now.Add(2*time.Minute), time.Minute)
	if err != nil || !takeover.Acquired || takeover.Epoch != 2 {
		t.Fatalf("takeover=%+v err=%v", takeover, err)
	}
	if err := ValidateLeader(context.Background(), db, "collector", "a", 1, now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale leader validated")
	}
}
