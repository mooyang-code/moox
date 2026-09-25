package bootstrap

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
)

func TestRetiredDatasetCheckID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		want bool
	}{
		{id: "dataset:collector:dataset_perpetual_kline_1h:1H", want: true},
		{id: "dataset:collector:dataset_binance_spot_kline_1h:1H", want: true},
		{id: "dataset:storage_view:view_crypto_spot_kline_1m:1m", want: true},
		{id: "dataset:collector:dataset_collector_df4b3afb3ff5547c:1m", want: true},
		{id: "dataset:storage_view:view_collector_e85209cd9eb4d363:1m", want: true},
		{id: "dataset:collector:dataset_binance_spot_kline_1m:1m", want: false},
		{id: "dataset:storage_view:view_binance_swap_kline_1m:1m", want: false},
		{id: "market_canary:dataset_binance_spot_kline_1m:BTC-USDT:1m:venue:binance", want: false},
		{id: "sysdeploy:control:storage-view", want: false},
	}
	for _, tc := range cases {
		if got := retiredDatasetCheckID(tc.id); got != tc.want {
			t.Fatalf("retiredDatasetCheckID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestEnsureDefaultCheckAlertRulesSkipsDisabledAndRetired(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	for _, check := range []domain.Check{
		{SpaceID: "crypto", CheckID: "dataset:collector:dataset_binance_spot_kline_1m:1m", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true},
		{SpaceID: "crypto", CheckID: "dataset:collector:dataset_perpetual_kline_1h:1H", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true},
		{CheckID: "sysdeploy:control:storage-view", Kind: domain.CheckKindHTTP, Source: domain.CheckSourceSysDeploy, Enabled: false},
	} {
		check := check
		if err := repositories.Checks.Create(ctx, &check); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Alerts.GetRule(ctx, "crypto", "default:dataset:collector:dataset_binance_spot_kline_1m:1m"); err != nil {
		t.Fatalf("kept 1m collector rule: %v", err)
	}
	if _, err := repositories.Alerts.GetRule(ctx, "crypto", "default:dataset:collector:dataset_perpetual_kline_1h:1H"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("retired 1H rule = %v, want not found", err)
	}
	if _, err := repositories.Alerts.GetRule(ctx, "", "default:sysdeploy:control:storage-view"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("disabled sysdeploy rule = %v, want not found", err)
	}
}

func TestRetireObsoleteBusinessChecksRemovesOneHourCollector(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	keep := domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:dataset_binance_spot_kline_1m:1m",
		Name: "keep", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true,
	}
	drop := domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:dataset_perpetual_kline_1h:1H",
		Name: "drop", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true,
	}
	for _, check := range []domain.Check{keep, drop} {
		check := check
		if err := repositories.Checks.Create(ctx, &check); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Alerts.CreateRule(ctx, &domain.AlertRule{
		SpaceID: drop.SpaceID, RuleID: "default:" + drop.CheckID, CheckID: drop.CheckID,
		FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := retireObsoleteBusinessChecks(ctx, repositories, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	got, err := repositories.Checks.Get(ctx, drop.SpaceID, drop.CheckID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("retired 1H check stayed enabled")
	}
	if _, err := repositories.Alerts.GetRule(ctx, drop.SpaceID, "default:"+drop.CheckID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("retired 1H rule = %v, want not found", err)
	}
	kept, err := repositories.Checks.Get(ctx, keep.SpaceID, keep.CheckID)
	if err != nil || !kept.Enabled {
		t.Fatalf("kept 1m check = %+v err=%v", kept, err)
	}
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
