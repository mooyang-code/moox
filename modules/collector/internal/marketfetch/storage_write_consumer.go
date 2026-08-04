package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

// StartStorageWriteConsumer consumes the durable Storage mutation stream and
// projects successful writes into task-instance freshness. It is separate from
// the Invoke completion consumer because Timer SCFs intentionally do not
// publish market-fetch completion events.
func StartStorageWriteConsumer(ctx context.Context, spaceID string, instances *store.TaskInstanceRepository) error {
	if instances == nil {
		return fmt.Errorf("task instance repository is required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("storage write consumer space_id is required")
	}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			client, err := completionEventBusClient(ctx)
			if err != nil {
				log.WarnContextf(ctx, "collector storage write EventBus unavailable; retrying: %v", err)
				if !sleepContext(ctx, backoff) {
					return
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			registry, err := events.DefaultRegistry()
			if err != nil {
				client.Close()
				log.WarnContextf(ctx, "collector storage write event registry unavailable; retrying: %v", err)
				if !sleepContext(ctx, backoff) {
					return
				}
				continue
			}
			consumer, err := events.NewSpaceConsumer(ctx, client, registry, events.SpaceConsumerConfig{ConsumerConfig: events.ConsumerConfig{Name: storageWriteConsumerName(spaceID), Event: events.DatasetRowsUpserted, AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 128, FetchMaxWait: 500 * time.Millisecond, DeliverDecodeErrors: true}, SpaceID: spaceID})
			if err != nil {
				client.Close()
				log.WarnContextf(ctx, "collector storage write consumer unavailable; retrying: %v", err)
				if !sleepContext(ctx, backoff) {
					return
				}
				continue
			}
			backoff = time.Second
			runStorageWriteConsumer(ctx, consumer, instances)
			consumer.Close()
			client.Close()
			if ctx.Err() == nil {
				_ = sleepContext(ctx, backoff)
			}
		}
	}()
	return nil
}

func storageWriteConsumerName(spaceID string) string {
	var name strings.Builder
	name.WriteString("collector-storage-write-v1-")
	for _, value := range spaceID {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '-' || value == '_' {
			name.WriteRune(value)
			continue
		}
		name.WriteByte('-')
	}
	return name.String()
}

func runStorageWriteConsumer(ctx context.Context, consumer *events.Consumer, instances *store.TaskInstanceRepository) {
	for ctx.Err() == nil {
		deliveries, fetchErr := consumer.FetchEvents(ctx, 16)
		if fetchErr != nil && len(deliveries) == 0 {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(fetchErr, nats.ErrTimeout) {
				continue
			}
			log.WarnContextf(ctx, "collector storage write event fetch failed: %v", fetchErr)
			return
		}
		for _, delivery := range deliveries {
			if delivery == nil || delivery.Delivery == nil {
				continue
			}
			if err := handleStorageWrite(ctx, instances, delivery); err != nil {
				if delivery.Err != nil {
					_ = delivery.Delivery.Term(ctx)
					continue
				}
				_ = delivery.Delivery.Nak(ctx, 5*time.Second)
				log.WarnContextf(ctx, "collector storage write event handling failed: %v", err)
				continue
			}
			_ = delivery.Delivery.Ack(ctx)
		}
	}
}

func handleStorageWrite(ctx context.Context, instances *store.TaskInstanceRepository, delivery *events.EventDelivery) error {
	if delivery.Err != nil {
		return fmt.Errorf("decode storage write event: %w", delivery.Err)
	}
	payload, ok := delivery.Payload.(*storagepb.DatasetRowsUpserted)
	if !ok || payload == nil {
		return fmt.Errorf("storage write event payload type is %T", delivery.Payload)
	}
	functionName := functionNameFromWriteSource(payload.GetWriteSource())
	if functionName == "" {
		return nil
	}
	at := time.Now().UTC()
	if delivery.Message != nil && delivery.Message.GetOccurredAt() != nil && delivery.Message.GetOccurredAt().CheckValid() == nil {
		at = delivery.Message.GetOccurredAt().AsTime().UTC()
	}
	observations := make([]store.StorageWriteObservation, 0, len(payload.GetRows()))
	for _, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		key := row.GetKey().GetTimeSeries()
		frequency, err := normalizeStorageFrequency(key.GetFreq())
		if err != nil {
			log.WarnContextf(ctx, "ignore storage write event with invalid frequency dataset=%s subject=%s frequency=%s: %v", payload.GetDatasetId(), key.GetSubjectId(), key.GetFreq(), err)
			continue
		}
		observations = append(observations, store.StorageWriteObservation{SpaceID: payload.GetSpaceId(), DatasetID: payload.GetDatasetId(), SubjectID: key.GetSubjectId(), Frequency: frequency, FunctionName: functionName, At: at})
	}
	updated, err := instances.MarkStorageWrites(ctx, observations)
	if err != nil {
		return fmt.Errorf("update task instance freshness: %w", err)
	}
	log.DebugContextf(ctx, "collector storage write observed space=%s dataset=%s source=%s rows=%d task_instances=%d", payload.GetSpaceId(), payload.GetDatasetId(), payload.GetWriteSource(), len(observations), updated)
	return nil
}
