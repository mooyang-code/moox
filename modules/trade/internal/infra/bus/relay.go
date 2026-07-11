package bus

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"time"
)

type Publisher interface {
	Publish(context.Context, *messagepb.MooxMessage, ...jetstream.PublishOption) (*jetstream.PublishAck, error)
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
		now := timestamppb.Now()
		payload, marshalErr := proto.Marshal(wrapperspb.Bytes(row.Payload))
		if marshalErr != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, marshalErr.Error())
			return marshalErr
		}
		kind := messagepb.MessageKind_MESSAGE_KIND_EVENT
		if row.Topic == "moox.trade.reconciliation.requested.v1" || row.Topic == "moox.trade.rebalance.requested.v1" {
			kind = messagepb.MessageKind_MESSAGE_KIND_COMMAND
		}
		msg := &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: row.MessageID, Topic: row.Topic, Kind: kind, Producer: &messagepb.Producer{ServiceName: "moox-trade", InstanceId: r.InstanceID, BootId: r.BootID, Version: "v2"}, OccurredAt: now, PublishedAt: now, ContentType: "application/x-protobuf; message=google.protobuf.BytesValue", Payload: payload}
		if _, e := r.Publisher.Publish(ctx, msg, jetstream.WithOrderingKey(row.MessageID)); e != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.ID, e.Error())
			return e
		}
		if e := r.Store.MarkOutboxPublished(ctx, row.ID); e != nil {
			return e
		}
	}
	return nil
}
