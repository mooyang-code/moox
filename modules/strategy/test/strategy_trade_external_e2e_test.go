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

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Only the upstream market-data read is substituted. Processor evaluates the
// persisted DSL, commits Result/Outbox atomically, and Relay publishes to NATS.
func TestExternalStrategyCommitPublishesLogicalAccountTarget(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	u, err := url.Parse(natsURL)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname(), "external E2E requires its isolated local NATS")
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.event.trade.target.weight_requested.v1.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo.Close() })
	require.NoError(t, repo.ApplySchema(schema.AllSQL()))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	bar := now.Truncate(time.Hour)
	dsl := `name: external-e2e
triggers:
  event: {name: factor.ready}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  rank:
    pool: [BTC-USDT, ETH-USDT]
    score: bias
    select: {top: 1}
    weight: "0.5"
`
	require.NoError(t, repo.SaveStrategyDefinition(ctx, store.StrategyDefinition{StrategyID: "strategy-e2e", StrategyName: "external-e2e", DSLYaml: dsl, CreatedAt: now, UpdatedAt: now}))
	logicalRaw, err := os.ReadFile(filepath.Join(os.Getenv("MOOX_STRATEGY_TRADE_E2E_COORD_DIR"), "logical-id"))
	require.NoError(t, err)
	logical, session := string(logicalRaw), "session-e2e"
	require.NotEmpty(t, logical)
	require.NoError(t, repo.CreateInstance(ctx, store.StrategyInstance{InstanceID: "instance-e2e", StrategyID: "strategy-e2e", SpaceID: "space-e2e", LogicalAccountID: &logical, InputBindingsJSON: json.RawMessage(`{"source_view_id":"source","frequency":"1h"}`), Enabled: true, SessionID: &session, CreatedAt: now, UpdatedAt: now}))
	endpoint, err := os.ReadFile(filepath.Join(os.Getenv("MOOX_STRATEGY_TRADE_E2E_COORD_DIR"), "trade-ready"))
	require.NoError(t, err)
	tradeURL, err := url.Parse(string(endpoint))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", tradeURL.Hostname())
	var authorizationReads int
	processor := &trigger.Processor{Store: repo, Loader: externalStrategyInput{}, Now: func() time.Time { return now }, Diagnostic: func(err error) { t.Logf("processor diagnostic: %v", err) },
		SessionGeneration: func(ctx context.Context, space, account, instance, session string) (int64, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(endpoint)+"/logical-account?"+url.Values{"space_id": {space}, "logical_account_id": {account}}.Encode(), nil)
			if err != nil {
				return 0, err
			}
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				return 0, err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			if err != nil {
				return 0, err
			}
			if response.StatusCode != http.StatusOK {
				return 0, fmt.Errorf("Trade authorization HTTP %d: %s", response.StatusCode, body)
			}
			var result tradepb.GetLogicalAccountRsp
			if err := protojson.Unmarshal(body, &result); err != nil {
				return 0, err
			}
			logical := result.GetLogicalAccount()
			if result.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || logical.GetOwnerInstanceId() != instance || logical.GetOwnerSessionId() != session || logical.GetAutomationState() != "ACTIVE" || logical.GetControlMode() != tradepb.ControlMode_CONTROL_MODE_STRATEGY {
				return 0, fmt.Errorf("Trade did not authorize instance %s session %s: %s", instance, session, body)
			}
			authorizationReads++
			return 0, nil
		},
	}
	event := trigger.PeriodReady{MessageID: "external-ready", EventName: "factor.ready", SpaceID: "space-e2e", ViewID: "factor", Frequency: "1h", PeriodTime: bar, BarEndTime: bar, Status: "complete"}
	require.NoError(t, processor.Handle(ctx, event))
	require.Equal(t, 1, authorizationReads)
	result, err := repo.LatestResult(ctx, "instance-e2e", session)
	require.NoError(t, err)
	require.Equal(t, store.PublishPending, result.PublishStatus)
	require.JSONEq(t, `[{"instrument_id":"BTC-USDT","target_weight":"0.5"}]`, string(result.TargetsJSON))
	require.NoError(t, processor.Handle(ctx, event), "input redelivery must not create a second result")
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
	message, err := registry.UnmarshalMessage(raw.Data)
	require.NoError(t, err)
	payload := new(tradeeventpb.LogicalAccountTargetWeightRequested)
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	require.Equal(t, result.ResultID, payload.GetTargetId())
	require.Equal(t, "instance-e2e", payload.GetInstanceId())
	require.Equal(t, session, payload.GetSessionId())
	require.Equal(t, "strategy-e2e", payload.GetStrategyId())
	require.Equal(t, logical, payload.GetLogicalAccountId())
	require.True(t, payload.GetBarEndTime().AsTime().Equal(bar))
	require.True(t, payload.GetEffectiveAt().AsTime().Equal(bar))
	require.True(t, payload.GetValidUntil().AsTime().After(now))
	t.Logf("modern Processor -> Result -> Relay -> NATS: target=%s instance=%s session=%s weights=%s", result.ResultID, payload.GetInstanceId(), payload.GetSessionId(), result.TargetsJSON)
}

type externalStrategyInput struct{}

func (externalStrategyInput) Load(_ context.Context, _ domain.StrategyRunner, _ compiler.CompiledStrategy, period time.Time) (input.EvaluationInput, error) {
	return input.EvaluationInput{SpaceID: "space-e2e", StrategyID: "strategy-e2e", PeriodEnd: period.Format(time.RFC3339Nano), SourceViewID: "source", DataFrequency: "1h", Items: []input.InstrumentInput{
		{PoolItem: input.PoolItem{InstrumentID: "BTC-USDT", SubjectID: "btc"}, Values: map[string]quant.Decimal{"bias": quant.Must("2")}},
		{PoolItem: input.PoolItem{InstrumentID: "ETH-USDT", SubjectID: "eth"}, Values: map[string]quant.Decimal{"bias": quant.Must("1")}},
	}}, nil
}
