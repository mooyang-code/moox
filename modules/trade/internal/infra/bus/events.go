package bus

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

func encode(registry *events.Registry, event events.EventType, eventID, spaceID, subjectID string, payload proto.Message, occurredAt time.Time) ([]byte, error) {
	return registry.MarshalMessage(event, payload, events.PublishOptions{EventID: eventID, OccurredAt: occurredAt, SpaceID: spaceID, SubjectID: subjectID})
}

func EncodeOrderSnapshot(event events.EventType, eventID string, r store.OrderRecord, occurredAt time.Time) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payload := &tradeeventpb.OrderSnapshot{OrderId: r.OrderID, ClientOrderId: r.ClientOrderID, AccountId: r.AccountID, ChannelId: r.ChannelID, Symbol: r.Symbol, MarketType: r.MarketType, BaseAsset: r.BaseAsset, QuoteAsset: r.QuoteAsset, Side: r.Side, Quantity: r.Quantity, Price: r.Price, ReduceOnly: r.ReduceOnly, FilledQuantity: r.FilledQuantity, State: r.State, ExchangeOrderId: r.ExchangeOrderID, Version: int64(r.Version)}
	return encode(registry, event, eventID, r.SpaceID, r.OrderID, payload, occurredAt)
}

func EncodeFillReceived(eventID, spaceID, fillID, orderID, accountID, channelID string, f exchange.FillEvent, occurredAt time.Time) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payload := &tradeeventpb.FillReceived{FillId: fillID, OrderId: orderID, AccountId: accountID, ChannelId: channelID, Symbol: f.Symbol, Side: f.Side, Quantity: f.Quantity.String(), Price: f.Price.String(), Fee: f.Fee.String(), FeeCurrency: f.FeeCurrency, TradedAtMs: normalizeTradedAtMillis(f.TradedAt)}
	return encode(registry, events.TradeFillReceived, eventID, spaceID, fillID, payload, occurredAt)
}

func EncodeRebalanceRequested(eventID string, run store.RebalanceRunRecord, occurredAt time.Time) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payload := &tradeeventpb.RebalanceRequested{RunId: run.RunID, AccountId: run.AccountID, ChannelId: run.ChannelID, MarketSnapshotId: run.MarketSnapshotID, PositionSnapshotId: run.PositionSnapshotID, RulesVersion: run.RulesVersion}
	return encode(registry, events.TradeRebalanceRequested, eventID, run.SpaceID, run.RunID, payload, occurredAt)
}

func EncodeRebalanceCompleted(eventID, spaceID, runID, status string, occurredAt time.Time) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payload := &tradeeventpb.RebalanceCompleted{RunId: runID, Status: status}
	return encode(registry, events.TradeRebalanceCompleted, eventID, spaceID, runID, payload, occurredAt)
}

func EncodeReconciliationRequested(eventID, spaceID, requestID, accountID, channelID string, occurredAt time.Time) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payload := &tradeeventpb.ReconciliationRequested{RequestId: requestID, AccountId: accountID, ChannelId: channelID}
	return encode(registry, events.TradeReconciliationRequested, eventID, spaceID, requestID, payload, occurredAt)
}

func normalizeTradedAtMillis(value int64) int64 {
	if value > 0 && value < 1_000_000_000_000 {
		return value * 1000
	}
	return value
}
