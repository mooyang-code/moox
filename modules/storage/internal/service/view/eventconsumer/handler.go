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

type DatasetPeriodCompletedHandler interface {
	HandleDatasetPeriodCompleted(context.Context, *eventpb.EventMessage, *storagepb.DatasetPeriodCompleted) error
}

type DatasetSyncPointHandler interface {
	HandleDatasetSyncPoint(context.Context, *eventpb.EventMessage, *storagepb.DatasetSyncPoint) error
}
