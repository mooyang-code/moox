package trigger

import (
	"context"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// PendingEventStore is the durable boundary between a live JetStream ACK and
// Factor's in-memory event windows.
type PendingEventStore interface {
	PutPendingEvent(context.Context, string, *storagepb.DatasetFieldsChanged, time.Time) error
	LoadPendingEvents(context.Context, func(string, *storagepb.DatasetFieldsChanged, time.Time) error) error
	DeletePendingEvents(context.Context, []string) error
}
