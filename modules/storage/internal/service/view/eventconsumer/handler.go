package eventconsumer

import (
	"context"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

type DatasetRowsHandler interface {
	HandleDatasetRows(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error
}

type DatasetRowsHandlerFunc func(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error

func (f DatasetRowsHandlerFunc) HandleDatasetRows(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.DatasetRowsUpserted) error {
	return f(ctx, message, payload)
}

// DeliveryLease keeps a live event ahead of a waiting backfill for the whole
// retry lifecycle, not just for one handler attempt.
type DeliveryLease interface {
	Acquire(context.Context) error
	Release()
}
