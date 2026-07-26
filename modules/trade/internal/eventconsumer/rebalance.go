package eventconsumer

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type RebalanceOptions struct {
	Client       *jetstream.Client
	ConsumerName string
	Store        *store.Store
	Engine       *command.Engine
	Wake         func()
}

func HandleRebalance(ctx context.Context, delivery *jetstream.Delivery, opts RebalanceOptions) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	request, ok := payload.(*tradeeventpb.RebalanceRequested)
	if !ok || request.GetRequestId() != message.GetEventId() || request.GetExecutionBindingId() != message.GetSubjectId() {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("%w: envelope identity mismatch", rebalanceapp.ErrInvalidRequest)}
	}
	processed, err := opts.Store.HasInbox(ctx, opts.ConsumerName, message.GetEventId())
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	if processed {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	planner := rebalanceapp.RequestPlanner{Resolver: SnapshotResolver{Store: opts.Store, Engine: opts.Engine}}
	input, err := planner.Build(ctx, message.GetSpaceId(), request)
	if err != nil {
		if rebalanceapp.IsPermanentRequestError(err) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	fresh, err := (rebalanceapp.Service{Store: opts.Store, Engine: opts.Engine}).CreateFromEvent(
		ctx,
		opts.ConsumerName,
		message.GetEventId(),
		message.GetEventName(),
		input,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	if fresh {
		if opts.Wake != nil {
			opts.Wake()
		} else {
			opts.Store.Wake()
		}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
