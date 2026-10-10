package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesSafeDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceID != "strategy-1" || cfg.EventBus.RelayInterval != time.Second || cfg.EventBus.RelayBatchSize != 100 || cfg.EventBus.ConnectTimeout != 3*time.Second || cfg.EventBus.ConsumerName != "strategy_view_data_ready_v1" {
		t.Fatalf("EventBus 默认值不符：%+v", cfg.EventBus)
	}
	if cfg.Trade.Timeout != tradeowner.DefaultTimeout {
		t.Fatalf("Trade 默认值不符：%+v", cfg.Trade)
	}
	if cfg.Evaluation.AttemptBudget != 5 || cfg.Retention.ResultItemsDays != 90 || cfg.Retention.ReplaysDays != 90 || cfg.Replay.ChunkBars != 50 || cfg.Replay.PageSize != 2000 || cfg.Replay.MissingPriceLiquidateBars != 3 {
		t.Fatalf("求值、保留与回放默认值不符：%+v %+v %+v", cfg.Evaluation, cfg.Retention, cfg.Replay)
	}
	if cfg.DependenciesConfigured() {
		t.Fatal("未配置 gateway_client 时不应视为已接线")
	}
}

func TestLoadReadsAppendedSections(t *testing.T) {
	cfg, err := Load(writeConfig(t, `database: ./strategy.sqlite
evaluation:
  attempt_budget: 7
retention:
  result_items_days: 30
  replays_days: 10
replay:
  chunk_bars: 20
  page_size: 500
  missing_price_liquidate_bars: 4
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Evaluation.AttemptBudget != 7 || cfg.Retention.ResultItemsDays != 30 || cfg.Retention.ReplaysDays != 10 || cfg.Replay.ChunkBars != 20 || cfg.Replay.PageSize != 500 || cfg.Replay.MissingPriceLiquidateBars != 4 {
		t.Fatalf("配置未生效：%+v %+v %+v", cfg.Evaluation, cfg.Retention, cfg.Replay)
	}
	for _, invalid := range []string{
		"evaluation:\n  attempt_budget: -1\n",
		"retention:\n  replays_days: -2\n",
		"replay:\n  chunk_bars: -5\n",
		"eventbus:\n  relay_interval: -1s\n",
	} {
		if _, err := Load(writeConfig(t, "database: ./strategy.sqlite\n"+invalid)); err == nil {
			t.Fatalf("非法配置应被拒绝：%s", invalid)
		}
	}
}

func TestLoadDerivesStorageAppKeysFromRuntimeSecrets(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-secret")
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\ngateway_client:\n  mode: local\n  caller: strategy\n  key_file: /secrets/caller-strategy.key\n  ca_file: /certs/moox-ca.crt\n  cache_dir: /tmp/gwcache\nstorage:\n  app_id: strategy\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.AppKey != serviceAuthKey("primary-secret", "strategy") || cfg.Storage.ViewAppKey != serviceAuthKey("view-secret", "strategy") {
		t.Fatalf("Storage 密钥派生不符：%+v", cfg.Storage)
	}
	if !cfg.DependenciesConfigured() {
		t.Fatal("配置 gateway_client 后应视为已接线")
	}
}

func TestLoadRejectsConfiguredStorageWithoutKeys(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	if _, err := Load(writeConfig(t, "gateway_client:\n  mode: local\n  caller: strategy\n  key_file: /secrets/caller-strategy.key\n  ca_file: /certs/moox-ca.crt\n  cache_dir: /tmp/gwcache\n")); err == nil {
		t.Fatal("缺少 app_key 应被拒绝")
	}
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	if _, err := Load(writeConfig(t, "gateway_client:\n  mode: local\n  caller: strategy\n  key_file: /secrets/caller-strategy.key\n  ca_file: /certs/moox-ca.crt\n  cache_dir: /tmp/gwcache\n")); err == nil {
		t.Fatal("缺少 view_app_key 应被拒绝")
	}
}
