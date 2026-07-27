package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
)

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
	go s.runNodeBatchLoop(ctx, batchSize, pollInterval)
	return nil
}

func (s *Service) runNodeBatchLoop(ctx context.Context, batchSize int, pollInterval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

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
			executeCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
			summary, executeErr := s.dispatchNodeBatchItemWithRetry(executeCtx, item, operation)
			cancel()
			if ctx.Err() != nil && (errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded)) {
				return fmt.Errorf(
					"node batch runtime stopped space=%s job=%s item=%s node=%s: %w",
					item.SpaceID, item.JobID, item.ItemID, item.NodeID, executeErr,
				)
			}

			completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), nodeBatchCompletionTimeout)
			defer completeCancel()
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
			return nil
		})
	}
	return trpc.GoAndWait(handlers...)
}

func (s *Service) dispatchNodeBatchItemWithRetry(
	ctx context.Context,
	item store.NodeBatchItem,
	operation string,
) (string, error) {
	var summary string
	var err error
	for attempt := 1; attempt <= nodeBatchProviderAttempts; attempt++ {
		summary, err = s.dispatchNodeBatchItem(ctx, item, operation)
		if err == nil || !isTransientSCFProviderError(err) || attempt == nodeBatchProviderAttempts {
			return summary, err
		}
		log.WarnContextf(
			ctx,
			"[CloudNode] transient node batch provider failure; retrying space=%s job=%s item=%s node=%s attempt=%d/%d error=%q",
			item.SpaceID, item.JobID, item.ItemID, item.NodeID, attempt, nodeBatchProviderAttempts, err,
		)
		if !waitNodeBatchPoll(ctx, nodeBatchProviderRetryDelay) {
			return summary, ctx.Err()
		}
	}
	return summary, err
}

func isTransientSCFProviderError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "clienterror.networkerror") ||
		strings.Contains(message, "requestlimitexceeded")
}

func (s *Service) dispatchNodeBatchItem(
	ctx context.Context,
	item store.NodeBatchItem,
	operation string,
) (string, error) {
	if s.executeNodeBatchItem != nil {
		return s.executeNodeBatchItem(ctx, item)
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
		return s.executeDeployNodeItem(ctx, item.SpaceID, request)
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
