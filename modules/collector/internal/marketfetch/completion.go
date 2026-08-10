package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/nats-io/nats.go"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

var errUnknownCompletionBatch = errors.New("unknown market fetch completion batch")
var errCompletionIdentityMismatch = errors.New("market fetch completion identity mismatch")

// StartCompletionConsumer binds the governed batch-completion family and
// returns immediately. The loop is best effort: if EventBus is unavailable,
// the timer's deadline recovery remains the source of truth.
func StartCompletionConsumer(ctx context.Context, spaceID string, batches *store.FetchBatchRepository, retries *store.FetchRetryRepository, instances *store.TaskInstanceRepository, metrics *Metrics) error {
	if batches == nil || retries == nil || instances == nil {
		return fmt.Errorf("completion repositories are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("completion consumer space_id is required")
	}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			client, err := completionEventBusClient(ctx)
			if err != nil {
				log.WarnContextf(ctx, "market fetch completion EventBus unavailable; retrying: %v", err)
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
				log.WarnContextf(ctx, "market fetch completion registry unavailable; retrying: %v", err)
				if !sleepContext(ctx, backoff) {
					return
				}
				continue
			}
			consumer, err := events.NewSpaceConsumer(ctx, client, registry, events.SpaceConsumerConfig{ConsumerConfig: events.ConsumerConfig{Name: completionConsumerName(spaceID), Event: events.MarketFetchBatchCompleted, AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 128, FetchMaxWait: 500 * time.Millisecond, DeliverDecodeErrors: true}, SpaceID: spaceID})
			if err != nil {
				client.Close()
				log.WarnContextf(ctx, "market fetch completion consumer unavailable; retrying: %v", err)
				if !sleepContext(ctx, backoff) {
					return
				}
				continue
			}
			backoff = time.Second
			runCompletionConsumer(ctx, consumer, batches, retries, instances, metrics)
			consumer.Close()
			client.Close()
			if ctx.Err() == nil {
				_ = sleepContext(ctx, backoff)
			}
		}
	}()
	return nil
}

func completionConsumerName(spaceID string) string {
	var name strings.Builder
	name.WriteString("collector-market-fetch-completion-v1-")
	for _, value := range spaceID {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '-' || value == '_' {
			name.WriteRune(value)
			continue
		}
		name.WriteByte('-')
	}
	return name.String()
}

func runCompletionConsumer(ctx context.Context, consumer *events.Consumer, batches *store.FetchBatchRepository, retries *store.FetchRetryRepository, instances *store.TaskInstanceRepository, metrics *Metrics) {
	for ctx.Err() == nil {
		deliveries, fetchErr := consumer.FetchEvents(ctx, 16)
		if fetchErr != nil && len(deliveries) == 0 {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(fetchErr, nats.ErrTimeout) {
				continue
			}
			log.WarnContextf(ctx, "market fetch completion fetch failed: %v", fetchErr)
			return
		}
		for _, delivery := range deliveries {
			if delivery == nil || delivery.Delivery == nil {
				continue
			}
			if err := handleCompletion(ctx, batches, retries, instances, metrics, delivery); err != nil {
				if delivery.Err != nil || errors.Is(err, errUnknownCompletionBatch) {
					_ = delivery.Delivery.Term(ctx)
					continue
				}
				if errors.Is(err, errCompletionIdentityMismatch) {
					_ = delivery.Delivery.Term(ctx)
					log.ErrorContextf(ctx, "market fetch completion rejected: %v", err)
					continue
				}
				_ = delivery.Delivery.Nak(ctx, 5*time.Second)
				log.WarnContextf(ctx, "market fetch completion handling failed: %v", err)
				continue
			}
			_ = delivery.Delivery.Ack(ctx)
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func handleCompletion(ctx context.Context, batches *store.FetchBatchRepository, retries *store.FetchRetryRepository, instances *store.TaskInstanceRepository, metrics *Metrics, delivery *events.EventDelivery) error {
	if delivery.Err != nil {
		return fmt.Errorf("decode market fetch completion: %w", delivery.Err)
	}
	payload, ok := delivery.Payload.(*marketfetchpb.MarketFetchBatchCompleted)
	if !ok || payload == nil {
		return fmt.Errorf("market fetch completion payload type is %T", delivery.Payload)
	}
	spaceID := delivery.Message.GetSpaceId()
	batch, err := batches.Get(ctx, spaceID, payload.GetBatchId())
	if err != nil {
		// Unknown completion is a poison message for this deployment, not a
		// reason to keep redelivering it forever.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %s: %v", errUnknownCompletionBatch, payload.GetBatchId(), err)
		}
		return fmt.Errorf("load completion batch %s: %w", payload.GetBatchId(), err)
	}
	if mismatch := completionIdentityMismatch(batch, payload); mismatch != "" {
		return fmt.Errorf("%w: %s", errCompletionIdentityMismatch, mismatch)
	}
	completedAt := time.Now().UTC()
	if payload.GetCompletedAt() != nil && payload.GetCompletedAt().CheckValid() == nil {
		completedAt = payload.GetCompletedAt().AsTime().UTC()
	}
	wasTimedOut := batch.Status == domain.BatchStatusTimedOut
	wasLateCompletion := batch.LateCompletion
	status := domain.BatchStatus(payload.GetStatus())
	if status == "" {
		status = domain.BatchStatusFailed
	}
	batch.Status = status
	if wasTimedOut {
		batch.Status = domain.BatchStatusTimedOut
		batch.LateCompletion = true
	}
	batch.SuccessCount = int(payload.GetSuccessCount())
	batch.RetryCount = int(payload.GetRetryCount())
	batch.PermanentFailedCount = int(payload.GetPermanentFailedCount())
	batch.ErrorSummary = payload.GetErrorSummary()
	batch.CompletedAt = &completedAt
	if batch.DeadlineAt != nil && completedAt.After(*batch.DeadlineAt) {
		batch.LateCompletion = true
	}
	effects := store.FetchCompletionEffects{}
	var original Request
	_ = json.Unmarshal([]byte(batch.RequestJSON), &original)
	lateCompletion := wasTimedOut || wasLateCompletion
	for _, item := range payload.GetItems() {
		if item == nil {
			continue
		}
		if key := item.GetSourceEventId(); key != "" {
			previous, getErr := retries.Get(ctx, spaceID, key)
			if getErr != nil && !errors.Is(getErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("load retry item %s: %w", key, getErr)
			}
			if previous != nil && previous.Status == "superseded" {
				// A newer realtime success already covered this target. An in-flight
				// older retry may still report completion, but cannot overwrite the
				// current task instance or schedule another retry.
				continue
			}
		}
		if item.GetOutcome() == string(domain.ItemOutcomeSuccess) && batch.BatchKind == domain.BatchKindRealtime {
			target, targetErr := time.Parse(time.RFC3339Nano, item.GetTargetDataTime())
			if targetErr == nil {
				effects.SupersedePendingRetries = append(effects.SupersedePendingRetries, store.MarketFetchRetrySupersede{SpaceID: spaceID, DatasetID: payload.GetDatasetId(), SubjectID: item.GetSubjectId(), Frequency: payload.GetFrequency(), TargetDataTime: target})
			}
		}
		if item.GetOutcome() == string(domain.ItemOutcomeSuccess) && (item.GetSourceEventId() != "" || lateCompletion) {
			key := item.GetSourceEventId()
			if key == "" {
				key = retryKey(payload.GetBatchId(), item.GetSubjectId(), item.GetTargetDataTime())
			}
			if lateCompletion {
				effects.CancelPendingRetryKeys = append(effects.CancelPendingRetryKeys, key)
			} else {
				effects.SucceededRetryKeys = append(effects.SucceededRetryKeys, key)
			}
			effects.InstanceUpdates = append(effects.InstanceUpdates, taskInstanceUpdate(spaceID, payload, item, completedAt, domain.InstanceStatusSuccess))
			continue
		}
		if item.GetOutcome() == string(domain.ItemOutcomeSuccess) {
			effects.InstanceUpdates = append(effects.InstanceUpdates, taskInstanceUpdate(spaceID, payload, item, completedAt, domain.InstanceStatusSuccess))
			continue
		}
		if !isRetryOutcome(item.GetOutcome()) {
			// A retry child which receives a permanent result must be terminally
			// resolved. Otherwise it remains dispatched forever and disappears
			// from both the retry scheduler and the failure metrics.
			if key := item.GetSourceEventId(); key != "" {
				effects.PermanentRetryKeys = append(effects.PermanentRetryKeys, key)
			}
			if !lateCompletion {
				effects.InstanceUpdates = append(effects.InstanceUpdates, taskInstanceUpdate(spaceID, payload, item, completedAt, domain.InstanceStatusFailed))
			}
			continue
		}
		key := item.GetSourceEventId()
		if key == "" {
			key = retryKey(payload.GetBatchId(), item.GetSubjectId(), item.GetTargetDataTime())
		}
		attempt := 1
		logicalSyncPointID := payload.GetBatchId()
		if previous, getErr := retries.Get(ctx, spaceID, key); getErr == nil && previous != nil {
			if lateCompletion {
				// The timeout recovery already owns this retry key. A late
				// retryable result from the original batch must not create a
				// second retry or move an in-flight child back to pending.
				continue
			}
			if previous.Status == "succeeded" || previous.Status == "permanent_failed" {
				// A late completion from an older batch must not resurrect a retry
				// item already resolved by a newer retry batch.
				continue
			}
			attempt = previous.Attempt + 1
			if previous.SourceBatchID != "" {
				// Keep the logical fence identity stable across every retry
				// generation. BatchID is intentionally new for each attempt.
				logicalSyncPointID = previous.SourceBatchID
			}
		} else if getErr != nil && !errors.Is(getErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load retry item %s: %w", key, getErr)
		}
		if attempt > maxRetryAttempts() {
			effects.PermanentRetryKeys = append(effects.PermanentRetryKeys, key)
			if !lateCompletion {
				effects.InstanceUpdates = append(effects.InstanceUpdates, taskInstanceUpdate(spaceID, payload, item, completedAt, domain.InstanceStatusFailed))
			}
			continue
		}
		when := completedAt.Add(retryDelay(attempt))
		target, _ := time.Parse(time.RFC3339Nano, item.GetTargetDataTime())
		if target.IsZero() {
			target = completedAt
		}
		collectionItem := retryCollectionItem(original, item, key)
		raw, _ := json.Marshal(collectionItem)
		effects.Retries = append(effects.Retries, &domain.RetryItem{SpaceID: spaceID, RetryKey: key, SourceBatchID: logicalSyncPointID, BatchKind: batch.BatchKind, DatasetID: collectionItem.DatasetID, SubjectID: collectionItem.SubjectID, Frequency: collectionItem.Frequency, TargetDataTime: target, TaskJSON: string(raw), Attempt: attempt, Status: "pending", NextRetryAt: &when, LastErrorType: item.GetErrorType(), LastErrorSummary: item.GetErrorSummary(), CreateTime: completedAt, ModifyTime: completedAt})
	}
	updated, err := batches.CompleteWithEffects(ctx, batch, effects)
	if err != nil {
		return err
	}
	if !updated {
		// Deadline recovery can win the CAS after the initial read above. Re-read
		// and apply the receipt as the one permitted late completion, preserving
		// its success/freshness while not recreating timeout-owned retries.
		current, getErr := batches.Get(ctx, spaceID, payload.GetBatchId())
		if getErr != nil || current.Status != domain.BatchStatusTimedOut || current.LateCompletion {
			return getErr
		}
		batch = current
		batch.Status = domain.BatchStatusTimedOut
		batch.LateCompletion = true
		batch.SuccessCount = int(payload.GetSuccessCount())
		batch.RetryCount = int(payload.GetRetryCount())
		batch.PermanentFailedCount = int(payload.GetPermanentFailedCount())
		batch.ErrorSummary = payload.GetErrorSummary()
		batch.CompletedAt = &completedAt
		effects.CancelPendingRetryKeys = append(effects.CancelPendingRetryKeys, effects.SucceededRetryKeys...)
		effects.SucceededRetryKeys = nil
		effects.Retries = nil
		effects.PermanentRetryKeys = nil
		updated, err = batches.CompleteWithEffects(ctx, batch, effects)
		if err != nil || !updated {
			return err
		}
	}
	if metrics != nil {
		metrics.Observe(spaceID, payload)
		if count, countErr := retries.CountPending(ctx, spaceID, payload.GetDatasetId(), payload.GetFrequency()); countErr == nil {
			metrics.SetRetryPending(spaceID, payload.GetDatasetId(), payload.GetFrequency(), int(count))
		}
	}
	return nil
}

func taskInstanceUpdate(spaceID string, payload *marketfetchpb.MarketFetchBatchCompleted, item *marketfetchpb.MarketFetchItemResult, at time.Time, status int) store.MarketFetchInstanceUpdate {
	resultData := map[string]any{"outcome": item.GetOutcome()}
	targetDataTime, err := time.Parse(time.RFC3339Nano, item.GetTargetDataTime())
	if err == nil {
		targetDataTime = targetDataTime.UTC()
		resultData["target_data_time"] = targetDataTime.Format(time.RFC3339Nano)
		resultData["target_data_unix"] = targetDataTime.Unix()
	}
	if item.GetErrorType() != "" {
		resultData["error_type"] = item.GetErrorType()
	}
	if item.GetErrorSummary() != "" {
		resultData["error_summary"] = item.GetErrorSummary()
	}
	result, _ := json.Marshal(resultData)
	return store.MarketFetchInstanceUpdate{
		SpaceID: spaceID, TaskID: item.GetTaskId(), DatasetID: payload.GetDatasetId(), SubjectID: item.GetSubjectId(), Frequency: payload.GetFrequency(), LastExecNode: payload.GetNodeId(), TargetDataTime: targetDataTime,
		At: at, Status: status, Result: string(result),
	}
}

func completionIdentityMismatch(batch *domain.BatchInvocation, payload *marketfetchpb.MarketFetchBatchCompleted) string {
	if batch == nil || payload == nil {
		return "batch or payload is nil"
	}
	checks := []struct{ name, expected, actual string }{
		{"schedule_id", batch.ScheduleID, payload.GetScheduleId()},
		{"batch_kind", string(batch.BatchKind), payload.GetBatchKind()},
		{"dataset_id", batch.DatasetID, payload.GetDatasetId()},
		{"frequency", batch.Frequency, payload.GetFrequency()},
	}
	// NodeID is intentionally not part of the completion identity. An invoke
	// timeout can trigger the same batch on a failover node, and its completion
	// is still valid for this batch. The batch ID and schedule scope prevent a
	// completion from being applied to a different planned batch.
	for _, check := range checks {
		if strings.TrimSpace(check.expected) != strings.TrimSpace(check.actual) {
			return fmt.Sprintf("%s expected=%q actual=%q", check.name, check.expected, check.actual)
		}
	}
	if int(payload.GetPlannedCount()) != batch.PlannedCount {
		return fmt.Sprintf("planned_count expected=%d actual=%d", batch.PlannedCount, payload.GetPlannedCount())
	}
	return ""
}

func maxRetryAttempts() int {
	return envInt("MOOX_FETCH_MAX_RETRY_ATTEMPTS", 3)
}

func completionEventBusClient(ctx context.Context) (*jetstream.Client, error) {
	cfg := jetstream.ConfigFromEnv(nil, "moox-collector-market-fetch-completion")
	if credentialFile := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_CREDENTIAL_FILE")); credentialFile != "" {
		if err := cfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(credentialFile)); err != nil {
			return nil, err
		}
	}
	return jetstream.Connect(ctx, cfg)
}

func isRetryOutcome(outcome string) bool {
	switch domain.ItemOutcome(outcome) {
	case domain.ItemOutcomeHTTP429, domain.ItemOutcomeHTTP5xx, domain.ItemOutcomeNetworkError, domain.ItemOutcomeStorageError:
		return true
	default:
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
	}
}

func retryKey(batchID, subjectID, target string) string {
	return strings.Join([]string{batchID, subjectID, target}, "|")
}

func retryCollectionItem(request Request, result *marketfetchpb.MarketFetchItemResult, key string) domain.CollectionItem {
	for _, item := range request.Items {
		if (result.GetTaskId() != "" && item.TaskID == result.GetTaskId()) || (item.SubjectID == result.GetSubjectId() && item.DatasetID == request.DatasetID) {
			item.SourceEventID = key
			return item
		}
	}
	return domain.CollectionItem{TaskID: result.GetTaskId(), SubjectID: result.GetSubjectId(), Symbol: result.GetSymbol(), TargetDataTime: result.GetTargetDataTime(), SourceEventID: key, DatasetID: request.DatasetID, Frequency: request.Frequency, Provider: request.Provider, MarketType: request.MarketType, DataType: "kline"}
}
