package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type Publisher interface {
	Publish(context.Context, events.EventType, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

type Relay struct {
	Store              *store.Store
	Publisher          Publisher
	InstanceID, BootID string
}

func (r Relay) RunOnce(ctx context.Context, limit int) error {
	rows, err := r.Store.ClaimOutbox(ctx, limit, 30*time.Second)
	if err != nil {
		return err
	}
	for _, row := range rows {
		event, err := eventForTopic(row.Topic)
		if err != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		payload, err := outboxStruct(row.Topic, row.Payload)
		if err != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		spaceID := stringField(payload, "space_id")
		if spaceID == "" {
			spaceID = "moox_system"
		}
		subjectID := firstField(payload, "subject_id", "symbol", "order_id", "run_id", "account_id")
		if subjectID == "" {
			subjectID = row.MessageID
		}
		if _, err := r.Publisher.Publish(ctx, event, payload, events.PublishOptions{EventID: row.MessageID, OccurredAt: time.Now().UTC(), SpaceID: spaceID, SubjectID: subjectID}); err != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, err.Error())
			return err
		}
		if err := r.Store.MarkOutboxPublished(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func eventForTopic(topic string) (events.EventType, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(topic), ".v1")
	switch normalized {
	case "moox.trade.order.intent.created":
		return events.TradeOrderIntentCreated, nil
	case "moox.trade.order.state.changed":
		return events.TradeOrderStateChanged, nil
	case "moox.trade.execution.slice.ready":
		return events.TradeExecutionSliceReady, nil
	case "moox.trade.fill.received":
		return events.TradeFillReceived, nil
	case "moox.trade.rebalance.requested":
		return events.TradeRebalanceRequested, nil
	case "moox.trade.rebalance.completed":
		return events.TradeRebalanceCompleted, nil
	case "moox.trade.reconciliation.requested":
		return events.TradeReconciliationRequested, nil
	default:
		return events.EventType{}, fmt.Errorf("trade outbox topic %q is not governed", topic)
	}
}

func outboxStruct(topic string, raw []byte) (*structpb.Struct, error) {
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		values = map[string]any{}
		switch strings.TrimSuffix(strings.TrimSpace(topic), ".v1") {
		case "moox.trade.fill.received":
			values["trade_id"] = string(raw)
		case "moox.trade.rebalance.completed":
			values["run_id"] = string(raw)
		default:
			return nil, fmt.Errorf("decode outbox payload for %q: %w", topic, err)
		}
	}
	return structpb.NewStruct(values)
}

func stringField(payload *structpb.Struct, key string) string {
	if payload == nil || payload.Fields[key] == nil {
		return ""
	}
	return payload.Fields[key].GetStringValue()
}

func firstField(payload *structpb.Struct, keys ...string) string {
	for _, key := range keys {
		if value := stringField(payload, key); value != "" {
			return value
		}
	}
	return ""
}
