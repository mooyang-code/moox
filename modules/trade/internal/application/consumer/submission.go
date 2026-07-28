package consumer

import (
	"context"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
)

type SubmissionService interface {
	Get(context.Context, string, string) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
}

// SubmissionWorker advances durable PENDING intents after Place has committed.
type SubmissionWorker struct{ Service SubmissionService }

func (w SubmissionWorker) Handle(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	current, err := w.Service.Get(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	if current.State != orderdomain.Pending {
		if current.State == orderdomain.SubmitUnknown ||
			current.State == orderdomain.Submitting {
			return w.Service.ResolveUnknown(ctx, spaceID, orderID)
		}
		return current, nil
	}
	return w.Service.Submit(ctx, spaceID, orderID)
}
