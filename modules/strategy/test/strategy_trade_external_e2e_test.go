//go:build e2e_external

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const externalDSL = `name: external_e2e
rules:
  - id: r
    type: rank
    score: "bias"
    select: {top: 1}
    weight: {total: 0.5}
portfolio:
  max_missing: 1
`

// 只替换上游行情读取：处理器求值固化的 DSL，原子写入结果与待投递事件，再由 Relay 发布到 NATS。
func TestExternalStrategyCommitPublishesLogicalAccountTarget(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	u, err := url.Parse(natsURL)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname(), "外部端到端测试只连接隔离的本机 NATS")
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.event.trade.target.weight_requested.v1.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo.Close() })
	require.NoError(t, repo.ApplySchema(schema.AllSQL()))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	barStart := now.Truncate(time.Hour).Add(-time.Hour)
	coord := os.Getenv("MOOX_STRATEGY_TRADE_E2E_COORD_DIR")
	logicalRaw, err := os.ReadFile(filepath.Join(coord, "logical-id"))
	require.NoError(t, err)
	logical, session := string(logicalRaw), "session-e2e"
	require.NotEmpty(t, logical)
	hash := dsl.Hash([]byte(externalDSL))
	require.NoError(t, repo.CreateDefinition(ctx, store.Definition{StrategyID: "strategy-e2e", Name: "external_e2e", DSLYaml: externalDSL, DSLHash: hash, CreatedAt: now}))
	require.NoError(t, repo.CreateInstance(ctx, store.Instance{InstanceID: "instance-e2e", StrategyID: "strategy-e2e", SpaceID: "space-e2e", ViewID: "factor_view", LogicalAccountID: &logical, CreatedAt: now}))
	resolved := input.Resolved{ViewID: "factor_view", DatasetID: "factor_ds", Bar: "1h", Calendar: input.DefaultCalendar, Spot: true, Columns: map[string]input.ColumnBinding{"bias": {Source: input.SourceFactor, FactorID: "bias", DefinitionHash: "sha256:bias"}}, Factors: map[string]string{"bias": "sha256:bias"}, ViewColumns: []string{"bias", "close"}}
	resolvedJSON, err := json.Marshal(resolved)
	require.NoError(t, err)
	require.NoError(t, repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "instance-e2e", DSLHash: hash, ResolvedJSON: string(resolvedJSON), CreatedAt: now}, externalDSL))
	require.NoError(t, repo.SetInstanceEnabled(ctx, "instance-e2e", false, &session, resolvedJSON, now))
	require.NoError(t, repo.SetInstanceEnabled(ctx, "instance-e2e", true, &session, resolvedJSON, now))

	// 启用时与进程启动时，Strategy 都要求 Trade 的账户所有者就是本实例本会话。
	endpoint, err := os.ReadFile(filepath.Join(coord, "trade-ready"))
	require.NoError(t, err)
	tradeURL, err := url.Parse(string(endpoint))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", tradeURL.Hostname())
	require.NoError(t, requireTradeAuthorization(ctx, string(endpoint), "space-e2e", logical, "instance-e2e", session))

	handler := &trigger.Handler{Store: repo, Loader: externalInput{}, Now: func() time.Time { return now }, Logf: t.Logf}
	message := &eventpb.EventMessage{EventId: "external-ready", EventName: "event.storage.view.data_ready", SpaceId: "space-e2e"}
	payload := &storagepb.ViewDataReady{ViewId: "factor_view", Frequency: "1h", PeriodTime: barStart.Unix(), Status: "complete", UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"}, Factors: []*storagepb.FactorPeriodState{{FactorId: "bias", Status: "complete", DefinitionHash: "sha256:bias"}}}
	require.NoError(t, handler.Handle(ctx, message, payload))
	result, found, err := repo.LatestOk(ctx, "instance-e2e", session)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.PublishPending, result.PublishStatus)
	require.JSONEq(t, `[{"instrument_id":"BTC-USDT","target_weight":"0.5"}]`, string(result.TargetsJSON))
	require.NoError(t, handler.Handle(ctx, message, payload), "重复投递不能产生第二个结果")
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-e2e-publisher"})
	require.NoError(t, err)
	managed, err := outbox.NewManagedClient(client)
	require.NoError(t, err)
	t.Cleanup(func() { _ = managed.Close() })
	relay := &outbox.Relay{Store: repo, Publisher: &outbox.JetStreamPublisher{Publisher: managed.EventPublisher(), InstanceID: "strategy-e2e"}}
	require.NoError(t, relay.PublishPending(ctx, 10))
	require.NoError(t, relay.PublishPending(ctx, 10))
	info, err := js.StreamInfo("MOOX_TRADE")
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs)
	raw, err := js.GetMsg("MOOX_TRADE", info.State.FirstSeq)
	require.NoError(t, err)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	decoded, err := registry.UnmarshalMessage(raw.Data)
	require.NoError(t, err)
	target := new(tradeeventpb.LogicalAccountTargetWeightRequested)
	require.NoError(t, proto.Unmarshal(decoded.GetPayload(), target))
	barEnd := barStart.Add(time.Hour)
	require.Equal(t, result.ResultID, target.GetTargetId())
	require.Equal(t, "instance-e2e", target.GetInstanceId())
	require.Equal(t, session, target.GetSessionId())
	require.Equal(t, "strategy-e2e", target.GetStrategyId())
	require.Equal(t, logical, target.GetLogicalAccountId())
	require.True(t, target.GetBarEndTime().AsTime().Equal(barEnd))
	require.True(t, target.GetEffectiveAt().AsTime().Equal(barEnd))
	require.True(t, target.GetValidUntil().AsTime().Equal(barEnd.Add(2*time.Hour)))
	t.Logf("Handler → Result → Relay → NATS：target=%s instance=%s session=%s weights=%s", result.ResultID, target.GetInstanceId(), target.GetSessionId(), result.TargetsJSON)
}

func requireTradeAuthorization(ctx context.Context, endpoint, space, account, instance, session string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/logical-account?"+url.Values{"space_id": {space}, "logical_account_id": {account}}.Encode(), nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Trade 授权查询返回 HTTP %d：%s", response.StatusCode, body)
	}
	var result tradepb.GetLogicalAccountRsp
	if err := protojson.Unmarshal(body, &result); err != nil {
		return err
	}
	logical := result.GetLogicalAccount()
	if result.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || logical.GetOwnerInstanceId() != instance || logical.GetOwnerSessionId() != session || logical.GetAutomationState() != "ACTIVE" || logical.GetControlMode() != tradepb.ControlMode_CONTROL_MODE_STRATEGY {
		return fmt.Errorf("Trade 没有授权实例 %s 的会话 %s：%s", instance, session, body)
	}
	return nil
}

// externalInput 是行情读取的替身：BTC 的 bias 高于 ETH。
type externalInput struct{}

func (externalInput) LoadBar(_ context.Context, _ string, resolved input.Resolved, _ *dsl.Program, bar input.Bar) (input.Loaded, error) {
	boundary, err := input.FromStorageStart(resolved.Calendar, resolved.Bar, bar.BarStart)
	if err != nil {
		return input.Loaded{}, err
	}
	ids := []string{"BTC-USDT", "ETH-USDT"}
	frame := engine.Frame{BarEnd: boundary.BarEnd, BarIndex: boundary.BarIndex, Spot: true, Universe: ids, Expected: map[string][]string{"r": ids}, AgedOut: map[string][]string{}, Rows: map[string]engine.Row{
		"BTC-USDT": {Values: map[string]float64{"bias": 2, "close": 100}},
		"ETH-USDT": {Values: map[string]float64{"bias": 1, "close": 10}},
	}}
	subjects := map[string]input.Subject{"BTC-USDT": {SubjectID: "BTC-USDT", Active: true}, "ETH-USDT": {SubjectID: "ETH-USDT", Active: true}}
	return input.Loaded{Frame: frame, Sets: input.Sets{Universe: ids, Expected: frame.Expected, AgedOut: frame.AgedOut, Subjects: subjects}, Boundary: boundary, IndexID: "idx", Revision: 1}, nil
}
