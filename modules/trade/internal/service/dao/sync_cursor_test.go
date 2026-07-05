package dao

import (
	"context"
	"testing"
	"time"

	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSyncCursorTestStore(t *testing.T) *GormStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(tradeschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return New(db, "0123456789abcdef0123456789abcdef")
}

func TestSyncCursorUpsertAndGet(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	cursor := &service.SyncCursor{
		AccountID:      "acc_1",
		ChannelID:      "ch_1",
		Exchange:       "binance",
		MarketType:     "spot",
		SyncType:       service.SyncTypeTrades,
		Symbol:         "GALAUSDT",
		CursorStartMS:  1000,
		CursorEndMS:    2000,
		LastSuccessAt:  time.Unix(2, 0),
		LastError:      "",
		IsEnabled:      true,
	}
	if err := store.UpsertSyncCursor(ctx, "crypto", cursor); err != nil {
		t.Fatalf("UpsertSyncCursor returned error: %v", err)
	}
	cursor.CursorEndMS = 3000
	if err := store.UpsertSyncCursor(ctx, "crypto", cursor); err != nil {
		t.Fatalf("second UpsertSyncCursor returned error: %v", err)
	}
	got, err := store.GetSyncCursor(ctx, "crypto", "acc_1", service.SyncTypeTrades, "GALAUSDT")
	if err != nil {
		t.Fatalf("GetSyncCursor returned error: %v", err)
	}
	if got.CursorEndMS != 3000 || got.Symbol != "GALAUSDT" || !got.IsEnabled {
		t.Fatalf("cursor = %+v, want updated cursor", got)
	}
}
