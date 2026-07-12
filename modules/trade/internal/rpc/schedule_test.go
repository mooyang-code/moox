package rpc

import (
	"testing"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/stretchr/testify/assert"
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

func TestApplySyncConfigDefaults_NilGlobalConfig_ShouldUseFallbackPageSize(t *testing.T) {
	config.SetGlobalConfig(nil)
	got := applySyncConfigDefaults(service.SyncOptions{})
	assert.Equal(t, 500, got.PageSize)
}

func TestApplySyncConfigDefaults_WithGlobalConfig_ShouldFillSections(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Sync.SyncTrades = false
	config.SetGlobalConfig(cfg)
	got := applySyncConfigDefaults(service.SyncOptions{SpaceID: "crypto"})
	assert.Equal(t, 24, got.WindowHours)
	assert.True(t, got.Sections[service.SyncTypeBalances])
	assert.False(t, got.Sections[service.SyncTypeTrades])
}

func TestParseSyncSections_ShouldSelectKnownSections(t *testing.T) {
	out := map[service.SyncType]bool{}
	parseSyncSections("balances,orders,unknown", out)
	assert.True(t, out[service.SyncTypeBalances])
	assert.True(t, out[service.SyncTypeOrders])
	assert.False(t, out[service.SyncTypeTrades])
}

func TestHandleSyncSchedule_NilService_ShouldNoop(t *testing.T) {
	SetDefaultSyncService(nil)
	err := HandleSyncSchedule(context.Background(), "space_id=test")
	assert.NoError(t, err)
}

func TestParseSyncScheduleParams_BareSpaceID_ShouldParse(t *testing.T) {
	got := parseSyncScheduleParams("crypto")
	assert.Equal(t, "crypto", got.SpaceID)
}
