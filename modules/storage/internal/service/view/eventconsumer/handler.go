package eventconsumer

import (
	"context"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

type DatasetRowsHandler interface {
	HandleDatasetRows(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error
}

// DatasetRowsBatchItem is one decoded rows event in a contiguous delivery
// batch. The event consumer only groups rows events from the same Dataset and
// never lets a period/sync marker overtake them.
type DatasetRowsBatchItem struct {
	Message *eventpb.EventMessage
	Payload *storagepb.DatasetRowsUpserted
}

// DatasetRowsBatchHandler is optional. It lets Storage View combine several
// rows deliveries into one index transaction while retaining the Dataset
// ordering fence used by markers and sync points.
type DatasetRowsBatchHandler interface {
	HandleDatasetRowsBatch(context.Context, []DatasetRowsBatchItem) error
}

type DatasetPeriodCollectedHandler interface {
	HandleDatasetPeriodCollected(context.Context, *eventpb.EventMessage, *storagepb.DatasetPeriodCollected) error
}

type FactorPeriodComputedHandler interface {
	HandleFactorPeriodComputed(context.Context, *eventpb.EventMessage, *storagepb.FactorPeriodComputed) error
}

type DatasetSyncPointHandler interface {
	HandleDatasetSyncPoint(context.Context, *eventpb.EventMessage, *storagepb.DatasetSyncPoint) error
}

// DeliveryLease keeps one live apply ahead of a waiting backfill. The policy
// acquires/releases it per attempt so a retrying delivery cannot deadlock a
// READY replacement that needs to acquire the writer side of the same gate.
type DeliveryLease interface {
	Acquire(context.Context) error
	Release()
}
