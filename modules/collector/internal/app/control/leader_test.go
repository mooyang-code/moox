package control

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"gorm.io/gorm"
)

func TestLeaderGuardMakesSecondControlPlaneStandby(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:control-leader?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	first, second := NewLeader(db, "first"), NewLeader(db, "second")
	first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
	if err := first.renew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.renew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.RequireLeader(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.RequireLeader(context.Background()); err == nil {
		t.Fatal("standby mutation guard succeeded")
	}
}
