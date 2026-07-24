package trigger

import (
	"context"
	"time"

	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// PendingEventStore is the durable boundary between a live JetStream ACK and
// Factor's in-memory event windows.
type PendingEventStore interface {
	// ClaimPendingEvent persists an unprocessed event and reports whether this
	// caller won the claim. Duplicate pending rows and processed redeliveries
	// must return claimed=false without exposing the event to memory.
	ClaimPendingEvent(context.Context, string, *storagepb.DatasetRowsUpserted, time.Time) (claimed bool, err error)
	LoadPendingEvents(context.Context, func(string, *storagepb.DatasetRowsUpserted, time.Time) error) error
	CommitPendingEvents(context.Context, []string) error
}
