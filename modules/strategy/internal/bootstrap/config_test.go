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
	t.Setenv("MOOX_TRADE_GATEWAY_URL", "")
	t.Setenv("MOOX_TRADE_GATEWAY_NODE_ID", "")
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceID != "strategy-1" || cfg.EventBus.RelayInterval != time.Second || cfg.EventBus.RelayBatchSize != 100 || cfg.EventBus.ConnectTimeout != 3*time.Second || cfg.EventBus.ConsumerName != "strategy_view_data_ready_v1" {
		t.Fatalf("EventBus 默认值不符：%+v", cfg.EventBus)
	}
	if cfg.Trade.GatewayURL != "" || cfg.Trade.Timeout != tradeowner.DefaultTimeout || cfg.Trade.Configured() {
		t.Fatalf("Trade 默认值不符：%+v", cfg.Trade)
	}
	if cfg.Evaluation.AttemptBudget != 5 || cfg.Retention.ResultItemsDays != 90 || cfg.Retention.ReplaysDays != 90 || cfg.Replay.ChunkBars != 50 || cfg.Replay.PageSize != 2000 || cfg.Replay.MissingPriceLiquidateBars != 3 {
		t.Fatalf("求值、保留与回放默认值不符：%+v %+v %+v", cfg.Evaluation, cfg.Retention, cfg.Replay)
	}
	if cfg.DependenciesConfigured() {
		t.Fatal("未配置 Factor 与 Storage 时不应视为已接线")
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
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\nstorage:\n  target: ip://storage:11003\n  app_id: strategy\nfactor:\n  target: ip://factor:11003\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.AppKey != serviceAuthKey("primary-secret", "strategy") || cfg.Storage.ViewAppKey != serviceAuthKey("view-secret", "strategy") {
		t.Fatalf("Storage 密钥派生不符：%+v", cfg.Storage)
	}
	if !cfg.DependenciesConfigured() {
		t.Fatal("Factor 与 Storage 都配置后应视为已接线")
	}
}

func TestLoadRejectsConfiguredStorageWithoutKeys(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	if _, err := Load(writeConfig(t, "storage:\n  target: ip://storage:11003\n")); err == nil {
		t.Fatal("缺少 app_key 应被拒绝")
	}
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	if _, err := Load(writeConfig(t, "storage:\n  target: ip://storage:11003\n")); err == nil {
		t.Fatal("缺少 view_app_key 应被拒绝")
	}
}

func TestLoadTradeGatewayConfiguration(t *testing.T) {
	t.Setenv("MOOX_TRADE_GATEWAY_URL", "")
	t.Setenv("MOOX_TRADE_GATEWAY_NODE_ID", "")
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"未接线", "", true},
		{"远端 HTTPS", "gateway_url: https://trade.example:11001\n  target_node: trade", true},
		{"本机 HTTP", "gateway_url: http://127.0.0.1:11002\n  target_node: control", true},
		{"远端明文", "gateway_url: http://trade.example:11002\n  target_node: trade", false},
		{"缺节点", "gateway_url: https://trade.example:11001", false},
		{"负超时", "timeout: -1s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "trade:\n  "+tc.fields+"\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("Load() = %v，期望 valid=%t", err, tc.valid)
			}
		})
	}
}

func TestLoadTradeGatewayUsesDedicatedOverrides(t *testing.T) {
	t.Setenv("MOOX_TRADE_GATEWAY_URL", "https://trade.example:11001")
	t.Setenv("MOOX_TRADE_GATEWAY_NODE_ID", "trade-node")
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "ip://127.0.0.1:11003")
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", "control")
	cfg, err := Load(writeConfig(t, "trade:\n  gateway_url: http://127.0.0.1:11002\n  target_node: control\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Trade.GatewayURL != "https://trade.example:11001" || cfg.Trade.TargetNode != "trade-node" || !cfg.Trade.Configured() {
		t.Fatalf("Trade 覆盖不符：%+v", cfg.Trade)
	}
	t.Setenv("MOOX_TRADE_GATEWAY_NODE_ID", "")
	if _, err := Load(writeConfig(t, "database: ./strategy.sqlite\n")); err == nil {
		t.Fatal("只设置 URL 不设置节点的环境变量应被拒绝")
	}
}

func TestStorageGatewayEndpointPrefersLocalStorageNode(t *testing.T) {
	t.Setenv("MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET", "ip://146.56.196.204:11003")
	t.Setenv("MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID", "storage")
	target, node := storageGatewayEndpoint(Config{Storage: RPCConfig{Target: "ip://127.0.0.1:11003", TargetNode: "storage-gateway"}})
	if target != "ip://146.56.196.204:11003" || node != "storage" {
		t.Fatalf("Storage 网关端点不符：%s %s", target, node)
	}
}
