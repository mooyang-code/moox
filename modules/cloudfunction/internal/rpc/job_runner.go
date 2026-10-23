package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/publishlease"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"google.golang.org/protobuf/encoding/protojson"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	functionJobCompletionTimeout  = 10 * time.Second
	functionJobProviderAttempts   = 3
	functionJobProviderRetryDelay = 500 * time.Millisecond
	// Each provider attempt gets its own operation deadline. The outer item
	// deadline leaves enough room for all attempts instead of making the first
	// timeout consume the context used by the remaining retries.
	functionJobItemTimeout = 16 * time.Minute
)

var errFunctionJobPublishSuperseded = errors.New("instance batch publish was superseded")

// StartFunctionJobRunner recovers interrupted work and starts the SQLite-backed
// instance batch loop. Startup recovery is synchronous so failures stop bootstrap.
func (s *Service) StartFunctionJobRunner(ctx context.Context, batchSize int, pollInterval time.Duration) error {
	if s == nil || s.catalog == nil {
		return fmt.Errorf("instance batch catalog is required")
	}
	if batchSize < 1 || batchSize > 10 {
		return fmt.Errorf("instance batch size must be between 1 and 10")
	}
	if pollInterval < 100*time.Millisecond || pollInterval > 10*time.Second {
		return fmt.Errorf("instance batch poll interval must be between 100ms and 10s")
	}
	if ctx == nil {
		return fmt.Errorf("instance batch runtime context is required")
	}
	recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), functionJobCompletionTimeout)
	defer recoveryCancel()
	requeued, err := s.catalog.RequeueInterruptedFunctionJobItems(recoveryCtx)
	if err != nil {
		return fmt.Errorf("requeue interrupted instance batch items: %w", err)
	}
	if requeued > 0 {
		log.InfoContextf(ctx, "[CloudFunction] requeued interrupted instance batch items count=%d", requeued)
	}
	s.functionJobWorkers.Add(1)
	go func() {
		defer s.functionJobWorkers.Done()
		s.runFunctionJobLoop(ctx, batchSize, pollInterval)
	}()
	return nil
}

// WaitFunctionJobRunner waits after cancellation before closing shared resources.
func (s *Service) WaitFunctionJobRunner() { s.functionJobWorkers.Wait() }

func (s *Service) runFunctionJobLoop(ctx context.Context, batchSize int, pollInterval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.releasePendingRecoveredPublishLeases(ctx, batchSize)

		items, err := s.catalog.TakePendingFunctionJobItems(ctx, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.ErrorContextf(ctx, "[CloudFunction] take instance batch items failed: %v", err)
			if !waitFunctionJobPoll(ctx, pollInterval) {
				return
			}
			continue
		}
		if len(items) == 0 {
			if !waitFunctionJobPoll(ctx, pollInterval) {
				return
			}
			continue
		}
		if s.functionJobTakenHook != nil {
			s.functionJobTakenHook(items)
		}
		if err := s.runTakenFunctionJob(ctx, items); err != nil {
			log.ErrorContextf(ctx, "[CloudFunction] instance batch infrastructure failure: %v", err)
			if ctx.Err() != nil {
				return
			}
			if _, requeueErr := s.catalog.RequeueInterruptedFunctionJobItems(context.WithoutCancel(ctx)); requeueErr != nil {
				log.ErrorContextf(ctx, "[CloudFunction] requeue interrupted instance batch items after failure: %v", requeueErr)
			}
		}
	}
}

func (s *Service) runTakenFunctionJob(ctx context.Context, items []store.FunctionJobItem) error {
	operations := make(map[string]string, len(items))
	for _, item := range items {
		key := item.SpaceID + "\x00" + item.JobID
		if _, ok := operations[key]; ok {
			continue
		}
		aggregate, err := s.catalog.GetFunctionJob(ctx, item.SpaceID, item.JobID)
		if err != nil {
			return fmt.Errorf("load instance batch operation space=%s job=%s: %w", item.SpaceID, item.JobID, err)
		}
		if aggregate == nil {
			return fmt.Errorf("instance batch not found: %s", item.JobID)
		}
		operations[key] = aggregate.Job.Operation
	}
	handlers := make([]func() error, 0, len(items))
	for index := range items {
		item := items[index]
		operation := operations[item.SpaceID+"\x00"+item.JobID]
		handlers = append(handlers, func() error {
			executeCtx, cancel := context.WithTimeout(ctx, functionJobItemTimeout)
			executeCtx = withFunctionJobDurableOperation(executeCtx)
			summary, executeErr := s.dispatchFunctionJobItemWithRetry(executeCtx, &item, operation)
			cancel()
			if ctx.Err() != nil && (errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded)) {
				return fmt.Errorf(
					"instance batch runtime stopped space=%s job=%s item=%s instance=%s: %w",
					item.SpaceID, item.JobID, item.ItemID, item.InstanceID, executeErr,
				)
			}

			completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), functionJobCompletionTimeout)
			defer completeCancel()
			if functionJobMutationOperation(operation) && !errors.Is(executeErr, errFunctionJobPublishSuperseded) &&
				(errors.Is(executeErr, errInstanceMutationReconciliationRequired) || errors.Is(executeErr, store.ErrInstanceMutationClaimed) || item.ResumeClaim && executeErr != nil) {
				if err := s.catalog.RequireFunctionJobItemReconciliation(
					completeCtx, item.SpaceID, item.JobID, item.ItemID, executeErr.Error(),
				); err != nil {
					return fmt.Errorf(
						"require instance batch reconciliation space=%s job=%s item=%s instance=%s: %w",
						item.SpaceID, item.JobID, item.ItemID, item.InstanceID, err,
					)
				}
				return nil
			}
			if err := s.catalog.CompleteFunctionJobItem(
				completeCtx,
				item.SpaceID,
				item.JobID,
				item.ItemID,
				summary,
				executeErr,
			); err != nil {
				return fmt.Errorf(
					"complete instance batch item space=%s job=%s item=%s instance=%s: %w",
					item.SpaceID, item.JobID, item.ItemID, item.InstanceID, err,
				)
			}
			if item.PublishLeaseRecoveryOwned {
				if err := s.releaseRecoveredPublishLease(completeCtx, &item, operation); err != nil {
					log.WarnContextf(completeCtx, "[CloudFunction] completed item but recovery lease release is pending space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
				}
			}
			return nil
		})
	}
	return trpc.GoAndWait(handlers...)
}

func (s *Service) dispatchFunctionJobItemWithRetry(
	ctx context.Context,
	item *store.FunctionJobItem,
	operation string,
) (string, error) {
	var summary string
	var err error
	ambiguousMutation := false
	takeoverAttempted := false
	for attempt := 1; attempt <= functionJobProviderAttempts; {
		attemptCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
		summary, err = s.dispatchFunctionJobItem(attemptCtx, item, operation)
		cancel()
		if errors.Is(err, errStaleCollectorPublishFenceBeforeOperation) {
			if takeoverAttempted {
				return summary, fmt.Errorf("%w: recovered publish lease was stale again before Provider operation: %w", errInstanceMutationReconciliationRequired, err)
			}
			takeoverAttempted = true
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, collectorPublishOperationRequestLimit)
			recoveryErr := s.recoverStaleFunctionJobPublishLease(recoveryCtx, item, operation)
			recoveryCancel()
			if recoveryErr != nil {
				if errors.Is(recoveryErr, publishlease.ErrLeaseSuperseded) {
					return summary, fmt.Errorf("%w: %w", errFunctionJobPublishSuperseded, recoveryErr)
				}
				return summary, fmt.Errorf("%w: recover stale Collector publish lease: %w", errInstanceMutationReconciliationRequired, recoveryErr)
			}
			continue
		}
		if functionJobMutationOperation(operation) && isAmbiguousSCFProviderOutcome(err) {
			ambiguousMutation = true
		}
		if err == nil {
			return summary, err
		}
		if !isTransientSCFProviderError(err) || attempt == functionJobProviderAttempts {
			if ambiguousMutation {
				return summary, fmt.Errorf("%w: %w", errInstanceMutationReconciliationRequired, err)
			}
			return summary, err
		}
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		delay := functionJobProviderRetryDelay
		for i := 1; i < attempt; i++ {
			delay *= 2
		}
		log.WarnContextf(
			ctx,
			"[CloudFunction] transient instance batch provider failure; retrying space=%s job=%s item=%s instance=%s attempt=%d/%d backoff=%s error=%q",
			item.SpaceID, item.JobID, item.ItemID, item.InstanceID, attempt, functionJobProviderAttempts, delay, err,
		)
		if !waitFunctionJobPoll(ctx, delay) {
			if ambiguousMutation {
				return summary, fmt.Errorf("%w: %w", errInstanceMutationReconciliationRequired, ctx.Err())
			}
			return summary, ctx.Err()
		}
		attempt++
	}
	return summary, err
}

func functionJobMutationOperation(operation string) bool {
	return operation == functionJobOperationCreate || operation == functionJobOperationDeploy ||
		operation == functionJobOperationDelete || operation == functionJobOperationRuntimeConfig
}

func (s *Service) recoverStaleFunctionJobPublishLease(ctx context.Context, item *store.FunctionJobItem, operation string) error {
	if s.publishLeaseValidator == nil {
		return fmt.Errorf("collector publish lease control plane is unavailable")
	}
	fence, required, err := s.functionJobPublishFence(ctx, *item, operation)
	if err != nil {
		return err
	}
	if !required {
		return fmt.Errorf("stale publish lease was reported for an unfenced instance batch item")
	}
	holderID := fmt.Sprintf("cloudfunction-recovery:%s:%s", item.JobID, item.ItemID)
	lease, err := s.publishLeaseValidator.AcquireLease(ctx, item.SpaceID, holderID, fence.fencingToken)
	if err != nil {
		return err
	}
	updated, err := s.catalog.ReplaceFunctionJobItemPublishFence(
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

func (s *Service) releaseRecoveredPublishLease(ctx context.Context, item *store.FunctionJobItem, operation string) error {
	if item == nil || !item.PublishLeaseRecoveryOwned {
		return nil
	}
	if s.publishLeaseValidator == nil {
		return fmt.Errorf("collector publish lease control plane is unavailable")
	}
	fence, err := functionJobRequestPublishFence(item, operation)
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
	return s.catalog.MarkFunctionJobItemPublishLeaseReleased(ctx, item.SpaceID, item.JobID, item.ItemID, lease.LeaseID, lease.FencingToken)
}

func (s *Service) releasePendingRecoveredPublishLeases(ctx context.Context, limit int) {
	if s.publishLeaseValidator == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), functionJobCompletionTimeout)
	defer cancel()
	items, err := s.catalog.ListTerminalFunctionJobItemsForPublishLeaseRelease(releaseCtx, limit)
	if err != nil {
		log.WarnContextf(ctx, "[CloudFunction] list completed recovery lease releases failed: %v", err)
		return
	}
	for i := range items {
		item := &items[i]
		aggregate, err := s.catalog.GetFunctionJob(releaseCtx, item.SpaceID, item.JobID)
		if err != nil || aggregate == nil {
			log.WarnContextf(ctx, "[CloudFunction] load completed batch for recovery lease release failed space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
			continue
		}
		if err := s.releaseRecoveredPublishLease(releaseCtx, item, aggregate.Job.Operation); err != nil {
			log.WarnContextf(ctx, "[CloudFunction] recovery-owned publish lease release remains pending space=%s job=%s item=%s: %v", item.SpaceID, item.JobID, item.ItemID, err)
		}
	}
}

func isTransientSCFProviderError(err error) bool {
	if err == nil {
		return false
	}
	// 等本地限流令牌的时间超过了本次尝试的截止时间：请求没有发出，下一次尝试有新的预算。
	if errors.Is(err, tencentscf.ErrRateLimitWaitExceedsDeadline) {
		return true
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

func (s *Service) dispatchFunctionJobItem(
	ctx context.Context,
	item *store.FunctionJobItem,
	operation string,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("instance batch item is required")
	}
	fence, required, err := s.functionJobPublishFence(ctx, *item, operation)
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
			return s.dispatchFunctionJobItemUnfenced(mutationCtx, item, operation)
		})
	}
	return s.dispatchFunctionJobItemUnfenced(ctx, item, operation)
}

func (s *Service) dispatchFunctionJobItemUnfenced(
	ctx context.Context,
	item *store.FunctionJobItem,
	operation string,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("instance batch item is required")
	}
	if s.executeFunctionJobItem != nil {
		return s.executeFunctionJobItem(ctx, *item)
	}
	switch operation {
	case functionJobOperationCreate:
		request := &pb.FunctionInstanceCreateItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode create instance item: %w", err)
		}
		return s.executeCreateInstanceItem(ctx, item.SpaceID, request, item.ItemIndex)
	case functionJobOperationDeploy:
		request := &pb.FunctionInstanceDeployItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode deploy instance item: %w", err)
		}
		if strings.TrimSpace(request.GetLifecycleId()) == "" || strings.TrimSpace(request.GetOperationId()) == "" {
			return "", fmt.Errorf("deploy item lifecycle identity is missing; resubmit the deployment")
		}
		return s.executeDeployInstanceItem(ctx, item.SpaceID, request)
	case functionJobOperationDelete:
		request := &pb.FunctionInstanceDeleteItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode delete instance item: %w", err)
		}
		return s.executeDeleteInstanceItem(ctx, item.SpaceID, request)
	case functionJobOperationRuntimeConfig:
		request := &pb.FunctionInstanceRuntimeConfigPatch{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return "", fmt.Errorf("decode runtime config item: %w", err)
		}
		return s.executeRuntimeConfigItem(ctx, item.SpaceID, request)
	default:
		return "", fmt.Errorf("unsupported instance batch operation %q", operation)
	}
}

func waitFunctionJobPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
