package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/publishlease"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	nodeBatchCompletionTimeout  = 10 * time.Second
	nodeBatchProviderAttempts   = 3
	nodeBatchProviderRetryDelay = 500 * time.Millisecond
	// Each provider attempt gets its own operation deadline. The outer item
	// deadline leaves enough room for all attempts instead of making the first
	// timeout consume the context used by the remaining retries.
	nodeBatchItemTimeout = 16 * time.Minute
)

var errNodeBatchPublishSuperseded = errors.New("node batch publish was superseded")

// StartNodeBatchRunner recovers interrupted work and starts the SQLite-backed
// node batch loop. Startup recovery is synchronous so failures stop bootstrap.
func (s *Service) StartNodeBatchRunner(ctx context.Context, batchSize int, pollInterval time.Duration) error {
	if s == nil || s.catalog == nil {
		return fmt.Errorf("node batch catalog is required")
	}
	if batchSize < 1 || batchSize > 10 {
		return fmt.Errorf("node batch size must be between 1 and 10")
	}
	if pollInterval < 100*time.Millisecond || pollInterval > 10*time.Second {
		return fmt.Errorf("node batch poll interval must be between 100ms and 10s")
	}
	if ctx == nil {
		return fmt.Errorf("node batch runtime context is required")
	}
	recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), nodeBatchCompletionTimeout)
	defer recoveryCancel()
	requeued, err := s.catalog.RequeueInterruptedNodeBatchItems(recoveryCtx)
	if err != nil {
		return fmt.Errorf("requeue interrupted node batch items: %w", err)
	}
	if requeued > 0 {
		log.InfoContextf(ctx, "[CloudNode] requeued interrupted node batch items count=%d", requeued)
	}
	recoveredItems, err := s.catalog.RecoverFailedNodeBatchMutationClaims(recoveryCtx)
	if err != nil {
		return fmt.Errorf("recover failed node batch mutation claims: %w", err)
	}
	if recoveredItems > 0 {
		log.InfoContextf(ctx, "[CloudNode] recovered failed node mutation items for reconciliation count=%d", recoveredItems)
	}
	repairedClaims, err := s.catalog.ReleaseTerminalNodeBatchClaims(recoveryCtx)
	if err != nil {
		return fmt.Errorf("repair terminal node batch claims: %w", err)
	}
	if repairedClaims > 0 {
		log.InfoContextf(ctx, "[CloudNode] repaired successful node batch mutation claims count=%d", repairedClaims)
	}
	s.nodeBatchWorkers.Add(1)
	go func() {
		defer s.nodeBatchWorkers.Done()
		s.runNodeBatchLoop(ctx, batchSize, pollInterval)
	}()
	return nil
}

// WaitNodeBatchRunner waits after cancellation before closing shared resources.
func (s *Service) WaitNodeBatchRunner() { s.nodeBatchWorkers.Wait() }

func (s *Service) runNodeBatchLoop(ctx context.Context, batchSize int, pollInterval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.releasePendingRecoveredPublishLeases(ctx, batchSize)

		items, err := s.catalog.TakePendingNodeBatchItems(ctx, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.ErrorContextf(ctx, "[CloudNode] take node batch items failed: %v", err)
			if !waitNodeBatchPoll(ctx, pollInterval) {
				return
			}
			continue
		}
		if len(items) == 0 {
			if !waitNodeBatchPoll(ctx, pollInterval) {
				return
			}
			continue
		}
		if s.nodeBatchTakenHook != nil {
			s.nodeBatchTakenHook(items)
		}
		if err := s.runTakenNodeBatch(ctx, items); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] node batch infrastructure failure: %v", err)
			if ctx.Err() != nil {
				return
			}
			if _, requeueErr := s.catalog.RequeueInterruptedNodeBatchItems(context.WithoutCancel(ctx)); requeueErr != nil {
				log.ErrorContextf(ctx, "[CloudNode] requeue interrupted node batch items after failure: %v", requeueErr)
			}
		}
	}
}

func (s *Service) runTakenNodeBatch(ctx context.Context, items []store.NodeBatchItem) error {
	operations := make(map[string]string, len(items))
	for _, item := range items {
		key := item.SpaceID + "\x00" + item.JobID
		if _, ok := operations[key]; ok {
			continue
		}
		aggregate, err := s.catalog.GetNodeBatch(ctx, item.SpaceID, item.JobID)
		if err != nil {
			return fmt.Errorf("load node batch operation space=%s job=%s: %w", item.SpaceID, item.JobID, err)
		}
		if aggregate == nil {
			return fmt.Errorf("node batch not found: %s", item.JobID)
		}
		operations[key] = aggregate.Job.Operation
	}
	handlers := make([]func() error, 0, len(items))
	for index := range items {
		item := items[index]
		operation := operations[item.SpaceID+"\x00"+item.JobID]
		handlers = append(handlers, func() error {
			executeCtx, cancel := context.WithTimeout(ctx, nodeBatchItemTimeout)
			executeCtx = withNodeBatchDurableOperation(executeCtx)
			summary, executeErr := s.dispatchNodeBatchItemWithRetry(executeCtx, &item, operation)
			cancel()
			if ctx.Err() != nil && (errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded)) {
				return fmt.Errorf(
					"node batch runtime stopped space=%s job=%s item=%s node=%s: %w",
					item.SpaceID, item.JobID, item.ItemID, item.NodeID, executeErr,
				)
			}

			completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), nodeBatchCompletionTimeout)
			defer completeCancel()
			if nodeBatchMutationOperation(operation) && !errors.Is(executeErr, errNodeBatchPublishSuperseded) &&
				(errors.Is(executeErr, errNodeMutationReconciliationRequired) || errors.Is(executeErr, store.ErrNodeMutationClaimed) || item.ResumeClaim && executeErr != nil) {
				if err := s.catalog.RequireNodeBatchItemReconciliation(
					completeCtx, item.SpaceID, item.JobID, item.ItemID, executeErr.Error(),
				); err != nil {
					return fmt.Errorf(
						"require node batch reconciliation space=%s job=%s item=%s node=%s: %w",
						item.SpaceID, item.JobID, item.ItemID, item.NodeID, err,
					)
				}
				return nil
			}
			if err := s.catalog.CompleteNodeBatchItem(
				completeCtx,
				item.SpaceID,
				item.JobID,
				item.ItemID,
				summary,
				executeErr,
			); err != nil {
				return fmt.Errorf(
					"complete node batch item space=%s job=%s item=%s node=%s: %w",
					item.SpaceID, item.JobID, item.ItemID, item.NodeID, err,
				)
			}
			if item.PublishLeaseRecoveryOwned {
				if err := s.releaseRecoveredPublishLease(completeCtx, &item, operation); err != nil {
					log.WarnContextf(completeCtx, "[CloudNode] completed item but recovery lease release is pending space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
				}
			}
			return nil
		})
	}
	return trpc.GoAndWait(handlers...)
}

func (s *Service) dispatchNodeBatchItemWithRetry(
	ctx context.Context,
	item *store.NodeBatchItem,
	operation string,
) (string, error) {
	var summary string
	var err error
	ambiguousMutation := false
	takeoverAttempted := false
	for attempt := 1; attempt <= nodeBatchProviderAttempts; {
		attemptCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
		summary, err = s.dispatchNodeBatchItem(attemptCtx, item, operation)
		cancel()
		if errors.Is(err, errStaleCollectorPublishFenceBeforeOperation) {
			if takeoverAttempted {
				return summary, fmt.Errorf("%w: recovered publish lease was stale again before Provider operation: %w", errNodeMutationReconciliationRequired, err)
			}
			takeoverAttempted = true
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, collectorPublishOperationRequestLimit)
			recoveryErr := s.recoverStaleNodeBatchPublishLease(recoveryCtx, item, operation)
			recoveryCancel()
			if recoveryErr != nil {
				if errors.Is(recoveryErr, publishlease.ErrLeaseSuperseded) {
					return summary, fmt.Errorf("%w: %w", errNodeBatchPublishSuperseded, recoveryErr)
				}
				return summary, fmt.Errorf("%w: recover stale Collector publish lease: %w", errNodeMutationReconciliationRequired, recoveryErr)
			}
			continue
		}
		if nodeBatchMutationOperation(operation) && isAmbiguousSCFProviderOutcome(err) {
			ambiguousMutation = true
		}
		if err == nil {
			return summary, err
		}
		if !isTransientSCFProviderError(err) || attempt == nodeBatchProviderAttempts {
			if ambiguousMutation {
				return summary, fmt.Errorf("%w: %w", errNodeMutationReconciliationRequired, err)
			}
			return summary, err
		}
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		delay := nodeBatchProviderRetryDelay
		for i := 1; i < attempt; i++ {
			delay *= 2
		}
		log.WarnContextf(
			ctx,
			"[CloudNode] transient node batch provider failure; retrying space=%s job=%s item=%s node=%s attempt=%d/%d backoff=%s error=%q",
			item.SpaceID, item.JobID, item.ItemID, item.NodeID, attempt, nodeBatchProviderAttempts, delay, err,
		)
		if !waitNodeBatchPoll(ctx, delay) {
			if ambiguousMutation {
				return summary, fmt.Errorf("%w: %w", errNodeMutationReconciliationRequired, ctx.Err())
			}
			return summary, ctx.Err()
		}
		attempt++
	}
	return summary, err
}

func nodeBatchMutationOperation(operation string) bool {
	return operation == nodeBatchOperationCreate || operation == nodeBatchOperationDeploy ||
		operation == nodeBatchOperationDelete || operation == nodeBatchOperationRuntimeConfig
}

func (s *Service) recoverStaleNodeBatchPublishLease(ctx context.Context, item *store.NodeBatchItem, operation string) error {
	if s.publishLeaseValidator == nil {
		return fmt.Errorf("collector publish lease control plane is unavailable")
	}
	fence, required, err := s.nodeBatchPublishFence(ctx, *item, operation)
	if err != nil {
		return err
	}
	if !required {
		return fmt.Errorf("stale publish lease was reported for an unfenced node batch item")
	}
	holderID := fmt.Sprintf("cloudnode-recovery:%s:%s", item.JobID, item.ItemID)
	lease, err := s.publishLeaseValidator.AcquireLease(ctx, item.SpaceID, holderID, fence.fencingToken)
	if err != nil {
		return err
	}
	updated, err := s.catalog.ReplaceNodeBatchItemPublishFence(
		ctx, item.SpaceID, item.JobID, item.ItemID, fence.leaseID, fence.fencingToken, lease.LeaseID, lease.FencingToken,
	)
	if err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectorPublishOperationRequestLimit)
		releaseErr := s.publishLeaseValidator.ReleaseLease(releaseCtx, lease)
		cancel()
		if errors.Is(releaseErr, publishlease.ErrLeaseStale) {
			releaseErr = nil
		}
		return errors.Join(err, releaseErr)
	}
	updated.ResumeClaim = item.ResumeClaim
	*item = *updated
	return nil
}

func (s *Service) releaseRecoveredPublishLease(ctx context.Context, item *store.NodeBatchItem, operation string) error {
	if item == nil || !item.PublishLeaseRecoveryOwned {
		return nil
	}
	if s.publishLeaseValidator == nil {
		return fmt.Errorf("collector publish lease control plane is unavailable")
	}
	fence, err := nodeBatchRequestPublishFence(item, operation)
	if err != nil {
		return err
	}
	lease := &publishlease.Lease{SpaceID: item.SpaceID, LeaseID: fence.leaseID, FencingToken: fence.fencingToken}
	err = s.publishLeaseValidator.ReleaseLease(ctx, lease)
	if errors.Is(err, publishlease.ErrLeaseStale) {
		err = nil
	}
	if err != nil {
		return err
	}
	return s.catalog.MarkNodeBatchItemPublishLeaseReleased(ctx, item.SpaceID, item.JobID, item.ItemID, lease.LeaseID, lease.FencingToken)
}

func (s *Service) releasePendingRecoveredPublishLeases(ctx context.Context, limit int) {
	if s.publishLeaseValidator == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeBatchCompletionTimeout)
	defer cancel()
	items, err := s.catalog.ListTerminalNodeBatchItemsForPublishLeaseRelease(releaseCtx, limit)
	if err != nil {
		log.WarnContextf(ctx, "[CloudNode] list completed recovery lease releases failed: %v", err)
		return
	}
	for i := range items {
		item := &items[i]
		aggregate, err := s.catalog.GetNodeBatch(releaseCtx, item.SpaceID, item.JobID)
		if err != nil || aggregate == nil {
			log.WarnContextf(ctx, "[CloudNode] load completed batch for recovery lease release failed space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
			continue
		}
		if err := s.releaseRecoveredPublishLease(releaseCtx, item, aggregate.Job.Operation); err != nil {
			log.WarnContextf(ctx, "[CloudNode] recovery-owned publish lease release remains pending space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
		}
	}
}

func isTransientSCFProviderError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"validation", "invalid param", "permission denied", "access denied", "not found", "quota", "limitexceeded.function"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	if strings.Contains(message, "clienterror.networkerror") ||
		strings.Contains(message, "requestlimitexceeded") ||
		strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "client.timeout") ||
		strings.Contains(message, "request canceled") ||
		strings.Contains(message, "context canceled") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "status code: 429") ||
		strings.Contains(message, "status code: 500") ||
		strings.Contains(message, "status code: 502") ||
		strings.Contains(message, "status code: 503") ||
		strings.Contains(message, "status code: 504") {
		return true
	}
	// Provider SDKs use different wording for transport timeouts. Keep the
	// generic fallback narrow so validation/quota errors are still fail-fast.
	return strings.Contains(message, " i/o timeout") || strings.HasSuffix(message, "timeout")
}

func (s *Service) dispatchNodeBatchItem(
	ctx context.Context,
	item *store.NodeBatchItem,
	operation string,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("node batch item is required")
	}
	fence, required, err := s.nodeBatchPublishFence(ctx, *item, operation)
	if err != nil {
		return "", err
	}
	if required {
		operationCtx := ctx
		if item.PublishLeaseRecoveryOwned {
			operationCtx = withRecoveredPublishLease(ctx, &publishlease.Lease{
				SpaceID: item.SpaceID, LeaseID: fence.leaseID, FencingToken: fence.fencingToken,
			})
		}
		return s.withCollectorPublishOperation(operationCtx, item.SpaceID, fence, func(mutationCtx context.Context) (string, error) {
			return s.dispatchNodeBatchItemUnfenced(mutationCtx, item, operation)
		})
	}
	return s.dispatchNodeBatchItemUnfenced(ctx, item, operation)
}

func (s *Service) dispatchNodeBatchItemUnfenced(
	ctx context.Context,
	item *store.NodeBatchItem,
	operation string,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("node batch item is required")
	}
	if s.executeNodeBatchItem != nil {
		return s.executeNodeBatchItem(ctx, *item)
	}
	switch operation {
	case nodeBatchOperationCreate:
		request := &pb.NodeCreateItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode create node item: %w", err)
		}
		return s.executeCreateNodeItem(ctx, item.SpaceID, request, item.ItemIndex)
	case nodeBatchOperationDeploy:
		request := &pb.NodeDeployItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode deploy node item: %w", err)
		}
		if strings.TrimSpace(request.GetLifecycleId()) == "" || strings.TrimSpace(request.GetOperationId()) == "" {
			return "", fmt.Errorf("deploy item lifecycle identity is missing; resubmit the deployment")
		}
		return s.executeDeployNodeItem(ctx, item.SpaceID, request)
	case nodeBatchOperationDelete:
		request := &pb.NodeDeleteItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode delete node item: %w", err)
		}
		return s.executeDeleteNodeItem(ctx, item.SpaceID, request)
	case nodeBatchOperationRuntimeConfig:
		request := &pb.NodeRuntimeConfigPatch{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode runtime config item: %w", err)
		}
		return s.executeRuntimeConfigItem(ctx, item.SpaceID, request)
	default:
		return "", fmt.Errorf("unsupported node batch operation %q", operation)
	}
}

func waitNodeBatchPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
