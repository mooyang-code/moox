package consumer

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// SubmissionWorker advances durable READY intents and resolves uncertain ones.
type SubmissionWorker struct{ Engine *command.Engine }

func (w SubmissionWorker) Handle(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := w.Engine.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	if r.State == string(order.SubmitUnknown) {
		return w.Engine.ResolveUnknown(ctx, space, orderID)
	}
	if r.State == string(order.Submitting) {
		return w.Engine.RecoverSubmitting(ctx, space, orderID)
	}
	if r.State != string(order.Ready) {
		return r, nil
	}
	return w.Engine.Submit(ctx, space, orderID, "")
}
