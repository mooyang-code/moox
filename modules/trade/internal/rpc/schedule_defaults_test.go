package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
)

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
