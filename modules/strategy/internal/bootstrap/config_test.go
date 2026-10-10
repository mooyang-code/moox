package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.GatewayClient.Caller != "strategy" || cfg.GatewayClient.KeyFile == "" {
		t.Fatalf("网关客户端默认身份不符：%+v", cfg.GatewayClient)
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
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\ngateway_client:\n  caller: strategy\n  key_file: /secrets/caller-strategy.key\nstorage:\n  app_id: strategy\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.AppKey != serviceAuthKey("primary-secret", "strategy") || cfg.Storage.ViewAppKey != serviceAuthKey("view-secret", "strategy") {
		t.Fatalf("Storage 密钥派生不符：%+v", cfg.Storage)
	}
}

// Load 只解析配置，不读取签名密钥；缺少 Storage 角色密钥在打开网关客户端时拒绝。
func TestOpenGatewayRequiresStorageKeys(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.OpenGateway(nil); err == nil || !strings.Contains(err.Error(), "app_key") {
		t.Fatalf("缺少 app_key 应被拒绝：%v", err)
	}
	cfg.Storage.AppKey = "primary"
	if _, err := cfg.OpenGateway(nil); err == nil || !strings.Contains(err.Error(), "view_app_key") {
		t.Fatalf("缺少 view_app_key 应被拒绝：%v", err)
	}
	if _, err := (Config{}).OpenGateway(nil); err == nil {
		t.Fatal("没有加载过配置时不能打开网关客户端")
	}
}

// 严格解析：已经删除的网关字段与错误的调用方身份都直接拒绝。
func TestLoadRejectsLegacyGatewayFieldsAndWrongCaller(t *testing.T) {
	for name, content := range map[string]string{
		"旧的 trade 网关地址":  "trade:\n  gateway_url: https://127.0.0.1:9\n  target_node: n1\n",
		"旧的 storage 目标":  "storage:\n  target: ip://127.0.0.1:11003\n",
		"调用方不是 strategy": "gateway_client:\n  caller: factor\n  key_file: /secrets/caller-strategy.key\n",
		"两个 YAML 文档":     "database: a\n---\ndatabase: b\n",
	} {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}
