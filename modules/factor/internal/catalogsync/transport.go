package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/nats-io/nats.go"
)

// SnapshotSubject must be restricted to the control/engine credentials in
// EventBus ACLs. This is not a public management endpoint.
const SnapshotSubject = "moox.factor.internal.catalog.snapshot"

type SnapshotReader interface {
	CatalogSnapshot(context.Context) (*domain.CatalogSnapshot, error)
}

type SnapshotConnection interface {
	Subscribe(string, nats.MsgHandler) (*nats.Subscription, error)
	RequestWithContext(context.Context, string, []byte) (*nats.Msg, error)
	FlushWithContext(context.Context) error
	MaxPayload() int64
}

type snapshotResponse struct {
	Snapshot *domain.CatalogSnapshot `json:"snapshot,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

func ServeSnapshots(ctx context.Context, connection SnapshotConnection, reader SnapshotReader) (*nats.Subscription, error) {
	if connection == nil || reader == nil {
		return nil, fmt.Errorf("catalog server requires a NATS connection and reader")
	}
	sub, err := connection.Subscribe(SnapshotSubject, func(message *nats.Msg) {
		if message.Reply == "" {
			return
		}
		response := snapshotResponse{}
		if len(message.Data) != 0 {
			response.Error = "invalid_request"
		} else {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			snapshot, readErr := reader.CatalogSnapshot(requestCtx)
			cancel()
			if readErr != nil || snapshot == nil {
				response.Error = "catalog_unavailable"
			} else {
				response.Snapshot = snapshot
			}
		}
		payload, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			payload = []byte(`{"error":"encoding_failed"}`)
		}
		if int64(len(payload)) > connection.MaxPayload() {
			payload = []byte(`{"error":"snapshot_too_large"}`)
		}
		// A lost response is retried by the puller; no catalog mutation occurs here.
		_ = message.Respond(payload)
	})
	if err != nil {
		return nil, err
	}
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = connection.FlushWithContext(flushCtx); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return sub, nil
}

func FetchSnapshot(ctx context.Context, connection SnapshotConnection) (*domain.CatalogSnapshot, error) {
	if connection == nil {
		return nil, fmt.Errorf("catalog client requires a NATS connection")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	message, err := connection.RequestWithContext(requestCtx, SnapshotSubject, nil)
	if err != nil {
		return nil, err
	}
	var response snapshotResponse
	if err := json.Unmarshal(message.Data, &response); err != nil {
		return nil, fmt.Errorf("decode catalog snapshot: %w", err)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("catalog request failed: %s", response.Error)
	}
	if response.Snapshot == nil || response.Snapshot.Revision < 0 {
		return nil, fmt.Errorf("invalid catalog snapshot response")
	}
	return response.Snapshot, nil
}
