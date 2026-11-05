package dispatch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const delistEvent = "event.collector.instrument.delist_scheduled"

var baseTime = time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	require.NoError(t, err)
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newIntake(t *testing.T, s *store.Store) *Intake {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	return &Intake{Store: s, Registry: registry, Now: func() time.Time { return baseTime }}
}

// rawEvent 是从 EventBus 取到的一条原始事件（测试里替代 JetStream 投递）。
type rawEvent struct {
	RawData      []byte
	Subject      string
	RawMessageID string
	ContentType  string
}

func handle(ctx context.Context, intake *Intake, event *rawEvent) Outcome {
	outcome, _ := intake.Process(ctx, event.RawData, event.Subject, event.RawMessageID, event.ContentType)
	return outcome
}

// delistDelivery 生成一条“标的即将下架”的投递；delistAt 不同、symbol 不同都会得到不同的事件 ID。
func delistDelivery(t *testing.T, exchange, market, symbol string, delistAt time.Time) *rawEvent {
	t.Helper()
	return delistDeliveryWithID(t, exchange, market, symbol, delistAt, events.InstrumentDelistEventID(exchange, market, symbol, events.DelistScopeAll, delistAt))
}

func delistDeliveryWithID(t *testing.T, exchange, market, symbol string, delistAt time.Time, eventID string) *rawEvent {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	payload := &storagepb.InstrumentDelistScheduled{
		Exchange: exchange, Market: market, ExchangeSymbol: symbol, DelistAt: timestamppb.New(delistAt),
		Source: events.DelistSourceManual, FirstSeenAt: timestamppb.New(baseTime), Scope: events.DelistScopeAll,
	}
	raw, err := registry.MarshalMessage(events.InstrumentDelistScheduled, payload, events.PublishOptions{
		EventID: eventID, OccurredAt: baseTime, SpaceID: events.DelistSystemSpaceID, SubjectID: events.InstrumentDelistSubjectID(exchange, market, symbol),
	})
	require.NoError(t, err)
	message, err := registry.UnmarshalMessage(raw)
	require.NoError(t, err)
	subject, err := registry.SubjectForMessage(message)
	require.NoError(t, err)
	return &rawEvent{RawData: raw, Subject: subject, RawMessageID: eventID, ContentType: events.ContentType}
}

func notifyRule(id string, scope ...store.ScopeFilter) store.Rule {
	return store.Rule{
		RuleID: id, Name: "规则 " + id, EventName: delistEvent, Scope: scope, ActionType: store.ActionNotifyURL,
		NotifyURL: "https://example.com/hook", Secret: "secret", Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}
}

func systemRule() store.Rule {
	return store.Rule{
		RuleID: "sys", Name: "系统规则", EventName: delistEvent, ActionType: store.ActionSystem,
		SystemAction: store.SystemActionSellDelistedHoldings, System: true, Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}
}

func listAll(t *testing.T, s *store.Store) []store.Execution {
	t.Helper()
	executions, _, err := s.ListExecutions(context.Background(), store.ExecutionFilter{}, 0, 100)
	require.NoError(t, err)
	return executions
}

func TestMatchesScope(t *testing.T) {
	entry, ok := catalog.Lookup(delistEvent)
	require.True(t, ok)
	message := &eventpb.EventMessage{SpaceId: "mooxsys", SubjectId: "binance:SPOT:FOOUSDT"}
	for name, test := range map[string]struct {
		scope []store.ScopeFilter
		want  bool
	}{
		"不限":          {nil, true},
		"交易所命中":       {[]store.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}}, true},
		"交易所多选里命中":    {[]store.ScopeFilter{{Field: "exchange", Values: []string{"okx", "binance"}}}, true},
		"交易所不命中":      {[]store.ScopeFilter{{Field: "exchange", Values: []string{"okx"}}}, false},
		"不同字段是且，都命中":  {[]store.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}, {Field: "market", Values: []string{"SPOT"}}}, true},
		"不同字段是且，一个不中": {[]store.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}, {Field: "market", Values: []string{"SWAP"}}}, false},
		"按代码":         {[]store.ScopeFilter{{Field: "exchange_symbol", Values: []string{"FOOUSDT"}}}, true},
		"取不到的字段不命中":   {[]store.ScopeFilter{{Field: "dataset", Values: []string{"x"}}}, false},
	} {
		require.Equal(t, test.want, Matches(store.Rule{Scope: test.scope}, entry, message), name)
	}
}

func TestIntakeCreatesOneExecutionPerMatchingRuleAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	require.NoError(t, s.CreateRule(ctx, notifyRule("binance-only", store.ScopeFilter{Field: "exchange", Values: []string{"binance"}})))
	require.NoError(t, s.CreateRule(ctx, notifyRule("okx-only", store.ScopeFilter{Field: "exchange", Values: []string{"okx"}})))
	require.NoError(t, s.CreateRule(ctx, notifyRule("everything")))
	disabled := notifyRule("disabled")
	disabled.Enabled = false
	require.NoError(t, s.CreateRule(ctx, disabled))

	delivery := delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", baseTime.Add(7*24*time.Hour))
	for range 3 { // 重投同一事件
		require.Equal(t, Accepted, handle(ctx, intake, delivery))
	}
	executions := listAll(t, s)
	require.Len(t, executions, 2, "只有命中的启用规则各建一条，重投不重复建")
	got := map[string]store.Execution{}
	for _, execution := range executions {
		got[execution.RuleID] = execution
	}
	require.Contains(t, got, "binance-only")
	require.Contains(t, got, "everything")
	execution := got["binance-only"]
	require.Equal(t, store.StatusPending, execution.Status)
	require.Equal(t, baseTime.UnixMilli(), execution.NextRunAt)
	require.Equal(t, "binance:SPOT:FOOUSDT", execution.SubjectID)
	require.Equal(t, events.DelistSystemSpaceID, execution.SpaceID)
	require.Equal(t, delistEvent, execution.EventName)
	require.Contains(t, execution.PayloadJSON, `"exchange_symbol":"FOOUSDT"`, "payload 转成蛇形命名的紧凑 JSON")
	require.Contains(t, execution.PayloadJSON, `"delist_at":"2026-11-11T08:00:00Z"`)
}

func TestIntakeWithoutMatchingRulesLeavesNoRecord(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	require.NoError(t, s.CreateRule(ctx, notifyRule("okx-only", store.ScopeFilter{Field: "exchange", Values: []string{"okx"}})))
	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", baseTime.Add(time.Hour))))
	require.Empty(t, listAll(t, s))
}

func TestSystemActionMergesSameInstrument(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	require.NoError(t, s.CreateRule(ctx, systemRule()))
	first := baseTime.Add(7 * 24 * time.Hour)

	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", first)))
	executions := listAll(t, s)
	require.Len(t, executions, 1)
	original := executions[0]
	require.Equal(t, "sys|binance:SPOT:FOOUSDT", original.DedupeKey)

	// 另一个标的是另一条记录。
	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "BARUSDT", first)))
	require.Len(t, listAll(t, s), 2)

	// 同一标的下架时间改了：合并进原记录，更新内容并立即重新安排，不新建。
	changed := first.Add(48 * time.Hour)
	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", changed)))
	executions = listAll(t, s)
	require.Len(t, executions, 2)
	merged, err := s.GetExecution(ctx, original.ExecutionID)
	require.NoError(t, err)
	require.Contains(t, merged.PayloadJSON, `"delist_at":"2026-11-13T08:00:00Z"`)
	require.Equal(t, original.EventID, merged.EventID, "记录保留最初那条事件的 ID")

	// 记录已经被取消：之后同一标的的新事件新建一条。
	_, err = s.CancelExecution(ctx, original.ExecutionID, baseTime.UnixMilli())
	require.NoError(t, err)
	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", changed.Add(time.Hour))))
	require.Len(t, listAll(t, s), 3)
}

func TestSystemActionIgnoresRepublishWithSameSchedule(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	require.NoError(t, s.CreateRule(ctx, systemRule()))
	delistAt := baseTime.Add(7 * 24 * time.Hour)
	require.Equal(t, Accepted, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", delistAt)))
	executions := listAll(t, s)
	require.Len(t, executions, 1)
	// 过了一段时间，工作线程把下一次推进安排到下架前 24 小时。
	require.NoError(t, s.InTx(ctx, func(tx *store.Tx) error {
		return tx.ReplaceExecutionEvent(executions[0].ExecutionID, executions[0].PayloadJSON, executions[0].OccurredAt, executions[0].NextRunAt+999)
	}))
	// 同样的下架时间被另一个事件再次报告（比如换了来源）：不动已经安排好的时间。
	repeat := delistDeliveryWithID(t, "binance", events.DelistMarketSpot, "FOOUSDT", delistAt, "event-from-another-source")
	require.Equal(t, Accepted, handle(ctx, intake, repeat))
	after := listAll(t, s)
	require.Len(t, after, 1)
	require.Equal(t, executions[0].NextRunAt+999, after[0].NextRunAt)
}

func TestUndecodableEventIsTerminated(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	valid := delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", baseTime.Add(time.Hour))
	for name, delivery := range map[string]*rawEvent{
		"乱码":       {RawData: []byte("not protobuf"), Subject: valid.Subject, RawMessageID: valid.RawMessageID, ContentType: events.ContentType},
		"主题不一致":    {RawData: valid.RawData, Subject: "moox.event.collector.instrument.delist_scheduled.v1.x.y", RawMessageID: valid.RawMessageID, ContentType: events.ContentType},
		"消息 ID 不符": {RawData: valid.RawData, Subject: valid.Subject, RawMessageID: "other", ContentType: events.ContentType},
	} {
		outcome, err := intake.Process(ctx, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
		require.Equal(t, Rejected, outcome, name)
		require.Error(t, err, name)
	}
}

func TestStoreFailureIsRetriedNotLost(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	intake := newIntake(t, s)
	require.NoError(t, s.CreateRule(ctx, notifyRule("all")))
	require.NoError(t, s.Close())
	require.Equal(t, RetryLater, handle(ctx, intake, delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", baseTime.Add(time.Hour))))
	require.Positive(t, RetryDelay)
}
