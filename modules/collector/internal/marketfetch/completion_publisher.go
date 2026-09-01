package marketfetch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// PublishCompletion publishes the durable batch fact used by the scheduler's
// completion consumer. Both Invoke and Timer callers use this single event
// boundary; the generic SCF entrypoint decides whether a call should publish.
func PublishCompletion(ctx context.Context, req Request, payload proto.Message) error {
	if strings.TrimSpace(req.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(nil, "moox-collector-market-fetch"))
	if err != nil {
		return err
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return err
	}
	subjectID := strings.TrimSpace(req.DatasetID)
	if subjectID == "" {
		subjectID = req.BatchID
	}
	_, err = publisher.Publish(ctx, events.MarketFetchBatchCompleted, payload, events.PublishOptions{
		EventID: req.BatchID, OccurredAt: time.Now().UTC(), SpaceID: req.SpaceID, SubjectID: subjectID,
	})
	if err != nil {
		log.ErrorContextf(ctx, "publish market fetch completion failed: batch_id=%s err=%v", req.BatchID, err)
	}
	return err
}
