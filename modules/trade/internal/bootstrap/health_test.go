package bootstrap

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
)

func TestTradeHealthSnapshot(t *testing.T) {
	cfg := config.DefaultConfig()
	rsp := tradeHealthSnapshot(cfg)(context.Background())

	if rsp.Module != "trade" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["sync_enabled"] != true {
		t.Fatalf("sync_enabled = %v", rsp.Details["sync_enabled"])
	}
}
