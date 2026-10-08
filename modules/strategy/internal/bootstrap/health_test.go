package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	strategyhealth "github.com/mooyang-code/moox/modules/strategy/internal/health"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}

var seedTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// seedEnabled 建立一个启用到会话 session-1 的实例，resolved 是固化的解析结果。
func seedEnabled(t *testing.T, repo *store.Store, instanceID string, account *string, resolved string) {
	t.Helper()
	ctx := context.Background()
	if _, err := repo.GetDefinition(ctx, "s1"); err != nil {
		if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: "sha256:demo", CreatedAt: seedTime}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: instanceID, StrategyID: "s1", SpaceID: "crypto", ViewID: "view_a", LogicalAccountID: account, CreatedAt: seedTime}); err != nil {
		t.Fatal(err)
	}
	session := instanceID + "-session"
	if err := repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: instanceID, DSLHash: "sha256:demo", ResolvedJSON: resolved, CreatedAt: seedTime}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, instanceID, true, &session, json.RawMessage(resolved), seedTime); err != nil {
		t.Fatal(err)
	}
}

const resolvedJSON = `{"view_id":"view_a","dataset_id":"ds","bar":"1h","calendar":"crypto_24x7","spot":true,"columns":{},"view_columns":["close"]}`

func TestRequireExecutionDependencies(t *testing.T) {
	repo := openStore(t)
	if err := requireExecutionDependencies(context.Background(), repo, Config{}); err != nil {
		t.Fatalf("没有启用实例时不应阻止启动：%v", err)
	}
	seedEnabled(t, repo, "observe", nil, resolvedJSON)
	if err := requireExecutionDependencies(context.Background(), repo, Config{}); err == nil || !strings.Contains(err.Error(), "Factor 与 Storage") {
		t.Fatalf("启用实例缺少依赖应阻止启动：%v", err)
	}
	wired := Config{Factor: RPCConfig{Target: "ip://f"}, Storage: RPCConfig{Target: "ip://s"}}
	if err := requireExecutionDependencies(context.Background(), repo, wired); err != nil {
		t.Fatalf("观察实例只需要 Factor 与 Storage：%v", err)
	}
	account := "acct-1"
	seedEnabled(t, repo, "trading", &account, resolvedJSON)
	if err := requireExecutionDependencies(context.Background(), repo, wired); err == nil || !strings.Contains(err.Error(), "Trade") {
		t.Fatalf("绑定账户的启用实例需要 Trade：%v", err)
	}
}

func TestStrategyHealthFailsClosedWhileEventBusUnavailable(t *testing.T) {
	repo := openStore(t)
	runtime, err := strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Store: repo, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Probe:     func(context.Context, strategyoutbox.JetStreamClient) error { return nil },
		Connector: func(context.Context) (strategyoutbox.JetStreamClient, error) { return nil, errors.New("unavailable") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	state := strategyhealth.New("strategy", "strategy", "", "")
	state.SetReady(true)
	response := strategyHealthSnapshot(repo, runtime, state, nil)(context.Background())
	if response.Ready {
		t.Fatal("EventBus 不可用时健康检查必须失败")
	}
	for _, key := range []string{"database_ready", "eventbus_connected", "ready_consumer_connected", "outbox_pending_count", "oldest_outbox_age_seconds"} {
		if _, ok := response.Details[key]; !ok {
			t.Fatalf("健康详情缺少 %q：%+v", key, response.Details)
		}
	}
}
