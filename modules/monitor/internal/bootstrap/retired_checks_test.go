package bootstrap

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsureDefaultCheckAlertRulesSkipsDisabledChecks(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	for _, check := range []domain.Check{
		{SpaceID: "crypto", CheckID: "dataset:collector:dataset_binance_kline_1m:1m", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true},
		{CheckID: "placement:control:storage-view", Kind: domain.CheckKindHTTP, Source: domain.CheckSourcePlacement, Enabled: false},
	} {
		check := check
		if err := repositories.Checks.Create(ctx, &check); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Alerts.GetRule(ctx, "crypto", "default:dataset:collector:dataset_binance_kline_1m:1m"); err != nil {
		t.Fatalf("kept 1m collector rule: %v", err)
	}
	if _, err := repositories.Alerts.GetRule(ctx, "", "default:placement:control:storage-view"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("disabled sysdeploy rule = %v, want not found", err)
	}
}

func TestRetireObsoleteBusinessChecksRemovesRulesOfDisabledChecks(t *testing.T) {
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	keep := domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:dataset_task:1m",
		Name: "keep", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true,
	}
	drop := domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:dataset_removed:1m",
		Name: "drop", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability,
	}
	for _, check := range []domain.Check{keep, drop} {
		check := check
		require.NoError(t, repositories.Checks.Create(ctx, &check))
		require.NoError(t, repositories.Alerts.CreateRule(ctx, &domain.AlertRule{
			SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID,
			FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
		}))
	}
	if err := repositories.Checks.Update(ctx, &domain.Check{ID: 2, SpaceID: drop.SpaceID, CheckID: drop.CheckID, Name: drop.Name, Kind: drop.Kind, Source: drop.Source, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.KlineFreshness.Enabled = true
	require.NoError(t, retireObsoleteBusinessChecks(ctx, repositories, cfg))
	if _, err := repositories.Alerts.GetRule(ctx, drop.SpaceID, "default:"+drop.CheckID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("rule of disabled check = %v, want not found", err)
	}
	_, err := repositories.Alerts.GetRule(ctx, keep.SpaceID, "default:"+keep.CheckID)
	require.NoError(t, err)
}

func TestRetireObsoleteBusinessChecksKeepsDynamicKlineChecksWhenEnabled(t *testing.T) {
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	check := domain.Check{
		SpaceID: "crypto", CheckID: "kline_freshness:crypto:task-owned-view:1m",
		Name: "dynamic kline", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true,
	}
	require.NoError(t, repositories.Checks.Create(ctx, &check))
	require.NoError(t, repositories.Alerts.CreateRule(ctx, &domain.AlertRule{
		SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID,
		FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
	}))
	cfg := &config.Config{}
	cfg.KlineFreshness.Enabled = true
	if err := retireObsoleteBusinessChecks(ctx, repositories, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := repositories.Checks.Get(ctx, check.SpaceID, check.CheckID)
	require.NoError(t, err)
	require.True(t, got.Enabled)
	_, err = repositories.Alerts.GetRule(ctx, check.SpaceID, "default:"+check.CheckID)
	require.NoError(t, err)
}

func openMonitorTestRepositories(t *testing.T) *store.Repositories {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	return manager.Repositories()
}
