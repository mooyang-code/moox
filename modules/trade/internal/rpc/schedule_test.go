package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

func TestParseSyncScheduleParams(t *testing.T) {
	got := parseSyncScheduleParams("space_id=crypto;account_id=acc_1;sections=balances,positions;window_hours=12;page_size=200;max_symbols=20")
	if got.SpaceID != "crypto" || got.AccountID != "acc_1" {
		t.Fatalf("ids = %#v", got)
	}
	if got.WindowHours != 12 || got.PageSize != 200 || got.MaxSymbolsPerRun != 20 {
		t.Fatalf("window/page/max = %#v", got)
	}
	if !got.Sections[service.SyncTypeBalances] || !got.Sections[service.SyncTypePositions] {
		t.Fatalf("sections = %#v", got.Sections)
	}
	if got.Sections[service.SyncTypeTrades] {
		t.Fatalf("trades should not be selected: %#v", got.Sections)
	}
}
