package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestTradeHealthSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rsp := tradeHealthSnapshot(db)(context.Background())

	if rsp.Module != "trade" || rsp.Ready || rsp.Status != "degraded" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["database_ready"] != true || rsp.Details["eventbus_ready"] != false {
		t.Fatalf("health details = %v", rsp.Details)
	}
}
