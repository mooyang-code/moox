package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	strategyhealth "github.com/mooyang-code/moox/modules/strategy/internal/health"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	// 按启用流程先把会话写到停用的实例上，再启用。
	if err := repo.SetInstanceEnabled(ctx, instanceID, false, &session, json.RawMessage(resolved), seedTime); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, instanceID, true, &session, json.RawMessage(resolved), seedTime); err != nil {
		t.Fatal(err)
	}
}

const resolvedJSON = `{"completion_kind":"collector.period.completed","view_id":"view_a","dataset_id":"ds","bar":"1h","calendar":"crypto_24x7","spot":true,"columns":{},"view_columns":["close"]}`

func TestRequireExecutionDependencies(t *testing.T) {
	repo := openStore(t)
	if err := requireExecutionDependencies(context.Background(), repo, Config{}); err != nil {
		t.Fatalf("没有启用实例时不应阻止启动：%v", err)
	}
	seedEnabled(t, repo, "observe", nil, resolvedJSON)
	if err := requireExecutionDependencies(context.Background(), repo, Config{}); err == nil || !strings.Contains(err.Error(), "gateway_client") {
		t.Fatalf("启用实例缺少依赖应阻止启动：%v", err)
	}
	wired := Config{GatewayClient: gatewayclient.Config{Mode: "local"}}
	if err := requireExecutionDependencies(context.Background(), repo, wired); err != nil {
		t.Fatalf("观察实例只需要 Factor 与 Storage：%v", err)
	}
	// 观察与绑定账户的实例都经同一个 gateway_client 访问 Factor、Storage 与 Trade。
	account := "acct-1"
	seedEnabled(t, repo, "trading", &account, resolvedJSON)
	if err := requireExecutionDependencies(context.Background(), repo, wired); err != nil {
		t.Fatalf("绑定账户的实例在配置 gateway_client 后应可启动：%v", err)
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

// fakeJetStream 是一直在线、但发布得不到确认的 EventBus 客户端。
type fakeJetStream struct{}

func (fakeJetStream) Ready() bool                                   { return true }
func (fakeJetStream) Close() error                                  { return nil }
func (fakeJetStream) EventPublisher() strategyoutbox.EventPublisher { return hangingPublisher{} }

type hangingPublisher struct{}

func (hangingPublisher) PublishMessage(ctx context.Context, _ *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// EventBus 在线但投递停滞（最老的待投递结果超过阈值仍未发出）时，健康检查报未就绪，Monitor 据此告警。
func TestStrategyHealthReportsStalledOutbox(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, resolvedJSON)
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	bar := timestamppb.New(now)
	data, err := registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "r1", InstanceId: "i1", StrategyId: "s1", SessionId: "i1-session", LogicalAccountId: "acct", BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(now.Add(time.Hour)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT", TargetWeight: "1"}},
	}, events.PublishOptions{EventID: "r1", OccurredAt: now, SpaceID: "crypto", SubjectID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-10 * time.Minute)
	insert := fmt.Sprintf(`INSERT INTO t_strategy_results (c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_dsl_hash, c_input_json, c_targets_json, c_rule_states_json, c_summary_json, c_event_data, c_publish_status, c_ctime)
		VALUES ('r1', 'i1', 'i1-session', %d, %d, 'ok', 'sha256:demo', '{}', '[]', '{}', '{}', X'%x', 'pending', '%s');`, now.UnixMilli(), now.Add(time.Hour).UnixMilli(), data, stale.Format("2006-01-02 15:04:05.999999999-07:00"))
	if err := repo.ApplySchema(insert); err != nil {
		t.Fatal(err)
	}
	runtime, err := strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Store: repo, RelayInterval: 10 * time.Millisecond, ReconnectInterval: 10 * time.Millisecond, BatchSize: 1, PublishTimeout: time.Hour,
		Probe:     func(context.Context, strategyoutbox.JetStreamClient) error { return nil },
		Connector: func(context.Context) (strategyoutbox.JetStreamClient, error) { return fakeJetStream{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !runtime.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	state := strategyhealth.New("strategy", "strategy", "", "")
	state.SetReady(true)
	response := strategyHealthSnapshot(repo, runtime, state, nil)(context.Background())
	if response.Ready || response.Details["outbox_stalled"] != true || response.Details["eventbus_connected"] != true {
		t.Fatalf("投递停滞时应报未就绪：%+v", response.Details)
	}
}
