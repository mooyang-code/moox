package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/publishlease"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
)

type CollectorPublishLeaseValidator interface {
	BeginOperation(context.Context, string, string, int64, string) error
	RenewOperation(context.Context, string, string, int64) error
	EndOperation(context.Context, string, string, int64) error
	AcquireLease(context.Context, string, string, int64) (*publishlease.Lease, error)
	RenewLease(context.Context, *publishlease.Lease) error
	ReleaseLease(context.Context, *publishlease.Lease) error
}

const (
	collectorPublishOperationRenewInterval = 30 * time.Second
	collectorPublishOperationRequestLimit  = 5 * time.Second
)

func requiresCollectorPublishLease(pkg *store.FunctionPackage) bool {
	return pkg != nil && strings.EqualFold(strings.TrimSpace(pkg.PackageType), "collector") &&
		strings.EqualFold(strings.TrimSpace(pkg.WorkloadType), "market_fetcher")
}

func collectorPublishLeaseFieldsPresent(leaseID string, token int64) bool {
	return strings.TrimSpace(leaseID) != "" && token > 0
}

func nodeBatchRequestPublishFence(item *store.NodeBatchItem, operation string) (collectorPublishFence, error) {
	if item == nil {
		return collectorPublishFence{}, fmt.Errorf("node batch item is required")
	}
	var leaseID string
	var token int64
	switch operation {
	case nodeBatchOperationCreate:
		request := &pb.NodeCreateItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, fmt.Errorf("decode create-node publish fence: %w", err)
		}
		leaseID, token = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	case nodeBatchOperationDeploy:
		request := &pb.NodeDeployItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, fmt.Errorf("decode deploy-node publish fence: %w", err)
		}
		leaseID, token = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	case nodeBatchOperationDelete:
		request := &pb.NodeDeleteItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, fmt.Errorf("decode delete-node publish fence: %w", err)
		}
		leaseID, token = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	case nodeBatchOperationRuntimeConfig:
		request := &pb.NodeRuntimeConfigPatch{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, fmt.Errorf("decode runtime-config publish fence: %w", err)
		}
		leaseID, token = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	default:
		return collectorPublishFence{}, fmt.Errorf("unsupported node batch operation %q", operation)
	}
	if !collectorPublishLeaseFieldsPresent(leaseID, token) {
		return collectorPublishFence{}, fmt.Errorf("node batch item has no complete publish fence")
	}
	return collectorPublishFence{leaseID: leaseID, fencingToken: token}, nil
}

type collectorPublishFence struct {
	leaseID      string
	fencingToken int64
}

type recoveredPublishLeaseContextKey struct{}

var errStaleCollectorPublishFenceBeforeOperation = errors.New("collector publish fence is stale before Provider operation")

func withRecoveredPublishLease(ctx context.Context, lease *publishlease.Lease) context.Context {
	if lease == nil {
		return ctx
	}
	return context.WithValue(ctx, recoveredPublishLeaseContextKey{}, lease)
}

func recoveredPublishLeaseFromContext(ctx context.Context) *publishlease.Lease {
	lease, _ := ctx.Value(recoveredPublishLeaseContextKey{}).(*publishlease.Lease)
	return lease
}

func (s *Service) nodeBatchPublishFence(ctx context.Context, item store.NodeBatchItem, operation string) (collectorPublishFence, bool, error) {
	var leaseID string
	var fencingToken int64
	requiresLease := false
	switch operation {
	case nodeBatchOperationCreate:
		request := &pb.NodeCreateItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, false, fmt.Errorf("decode fenced create-node request: %w", err)
		}
		if strings.TrimSpace(request.GetPackageId()) == "" {
			return collectorPublishFence{}, false, nil
		}
		pkg, err := s.catalog.GetPackage(ctx, item.SpaceID, request.GetPackageId())
		if err != nil {
			return collectorPublishFence{}, false, err
		}
		requiresLease = requiresCollectorPublishLease(pkg)
		leaseID, fencingToken = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	case nodeBatchOperationDeploy:
		request := &pb.NodeDeployItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, false, fmt.Errorf("decode fenced deploy request: %w", err)
		}
		if strings.TrimSpace(request.GetNodeId()) == "" || strings.TrimSpace(request.GetPackageId()) == "" {
			return collectorPublishFence{}, false, nil
		}
		pkg, err := s.catalog.GetPackage(ctx, item.SpaceID, request.GetPackageId())
		if err != nil {
			return collectorPublishFence{}, false, err
		}
		requiresLease = requiresCollectorPublishLease(pkg)
		node, nodeErr := s.catalog.GetNode(ctx, item.SpaceID, request.GetNodeId())
		if nodeErr != nil {
			return collectorPublishFence{}, false, nodeErr
		}
		if isFencedCollectorNode(node, nil) {
			requiresLease = true
		} else if node != nil && !requiresLease {
			oldPackage, packageErr := s.catalog.GetPackage(ctx, item.SpaceID, node.PackageID)
			if packageErr != nil {
				return collectorPublishFence{}, false, packageErr
			}
			requiresLease = requiresCollectorPublishLease(oldPackage)
		}
		leaseID, fencingToken = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	case nodeBatchOperationDelete:
		request := &pb.NodeDeleteItem{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, false, fmt.Errorf("decode fenced delete request: %w", err)
		}
		leaseID, fencingToken = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
		// A durable deletion submitted under a publish lease stays fenced even
		// if its node disappeared before the worker claims the operation. The
		// lifecycle identity check in executeDeleteNodeItem then rejects a
		// replacement generation without ever touching its provider function.
		requiresLease = collectorPublishLeaseFieldsPresent(leaseID, fencingToken)
		node, err := s.catalog.GetNode(ctx, item.SpaceID, request.GetNodeId())
		if err != nil {
			return collectorPublishFence{}, false, err
		}
		requiresLease = requiresLease || isFencedCollectorNode(node, nil)
		if node != nil && !requiresLease {
			pkg, packageErr := s.catalog.GetPackage(ctx, item.SpaceID, node.PackageID)
			if packageErr != nil {
				return collectorPublishFence{}, false, packageErr
			}
			requiresLease = requiresCollectorPublishLease(pkg)
		}
	case nodeBatchOperationRuntimeConfig:
		request := &pb.NodeRuntimeConfigPatch{}
		if err := protojson.Unmarshal([]byte(item.RequestJSON), request); err != nil {
			return collectorPublishFence{}, false, fmt.Errorf("decode fenced runtime-config request: %w", err)
		}
		if strings.TrimSpace(request.GetNodeId()) == "" {
			return collectorPublishFence{}, false, nil
		}
		node, err := s.catalog.GetNode(ctx, item.SpaceID, request.GetNodeId())
		if err != nil {
			return collectorPublishFence{}, false, err
		}
		if node != nil {
			pkg, packageErr := s.catalog.GetPackage(ctx, item.SpaceID, node.PackageID)
			if packageErr != nil {
				return collectorPublishFence{}, false, packageErr
			}
			requiresLease = isFencedCollectorNode(node, pkg)
		}
		leaseID, fencingToken = request.GetCollectorPublishLeaseId(), request.GetCollectorPublishFencingToken()
	default:
		return collectorPublishFence{}, false, nil
	}
	if !requiresLease {
		return collectorPublishFence{}, false, nil
	}
	if !collectorPublishLeaseFieldsPresent(leaseID, fencingToken) {
		return collectorPublishFence{}, false, fmt.Errorf("collector market_fetcher SCF mutation requires a publish lease and fencing token")
	}
	return collectorPublishFence{leaseID: leaseID, fencingToken: fencingToken}, true, nil
}

func (s *Service) withCollectorPublishOperation(ctx context.Context, spaceID string, fence collectorPublishFence, execute func(context.Context) (string, error)) (string, error) {
	return s.withCollectorPublishOperationRenewalInterval(ctx, spaceID, fence, collectorPublishOperationRenewInterval, execute)
}

func (s *Service) withCollectorPublishOperationRenewalInterval(ctx context.Context, spaceID string, fence collectorPublishFence, renewInterval time.Duration, execute func(context.Context) (string, error)) (string, error) {
	if s.publishLeaseValidator == nil {
		return "", fmt.Errorf("collector publish lease control plane is unavailable")
	}
	if renewInterval <= 0 {
		return "", fmt.Errorf("collector publish lease renewal interval must be positive")
	}
	recoveryLease := recoveredPublishLeaseFromContext(ctx)
	if recoveryLease != nil {
		renewCtx, renewCancel := context.WithTimeout(ctx, collectorPublishOperationRequestLimit)
		err := s.publishLeaseValidator.RenewLease(renewCtx, recoveryLease)
		renewCancel()
		if err != nil {
			if errors.Is(err, publishlease.ErrLeaseStale) {
				return "", fmt.Errorf("%w: renew recovery-owned collector publish lease before operation: %w", errStaleCollectorPublishFenceBeforeOperation, err)
			}
			return "", fmt.Errorf("%w: renew recovery-owned collector publish lease before operation: %w", errNodeMutationReconciliationRequired, err)
		}
	}
	operationID := uuid.NewString()
	if err := s.publishLeaseValidator.BeginOperation(ctx, spaceID, fence.leaseID, fence.fencingToken, operationID); err != nil {
		if errors.Is(err, publishlease.ErrLeaseStale) {
			return "", fmt.Errorf("%w: %w", errStaleCollectorPublishFenceBeforeOperation, err)
		}
		return "", fmt.Errorf("collector publish lease validation failed: %w", err)
	}
	executeCtx, cancel := context.WithCancel(ctx)
	renewDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-executeCtx.Done():
				renewDone <- nil
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(context.WithoutCancel(executeCtx), collectorPublishOperationRequestLimit)
				if recoveryLease != nil {
					if err := s.publishLeaseValidator.RenewLease(renewCtx, recoveryLease); err != nil {
						renewCancel()
						cancel()
						renewDone <- fmt.Errorf("renew recovery-owned collector publish lease: %w", err)
						return
					}
				}
				err := s.publishLeaseValidator.RenewOperation(renewCtx, spaceID, operationID, fence.fencingToken)
				renewCancel()
				if err != nil {
					cancel()
					renewDone <- err
					return
				}
			}
		}
	}()
	result, executeErr := execute(executeCtx)
	cancel()
	renewErr := <-renewDone
	if renewErr != nil {
		executeErr = errors.Join(executeErr, fmt.Errorf("renew collector publish operation claim: %w", renewErr))
	}
	if renewErr != nil || isAmbiguousSCFProviderOutcome(executeErr) {
		// The provider may have accepted the request even if CloudNode lost the
		// response. Keep the durable claim until its TTL so a higher fencing token
		// cannot race a late provider-side mutation.
		if renewErr != nil {
			return result, fmt.Errorf("%w: %w", errNodeMutationReconciliationRequired, executeErr)
		}
		return result, executeErr
	}
	endCtx, endCancel := context.WithTimeout(context.WithoutCancel(ctx), collectorPublishOperationRequestLimit)
	endErr := s.publishLeaseValidator.EndOperation(endCtx, spaceID, operationID, fence.fencingToken)
	endCancel()
	if endErr != nil {
		executeErr = errors.Join(executeErr, fmt.Errorf("end collector publish operation claim: %w", endErr))
	}
	return result, executeErr
}

func isAmbiguousSCFProviderOutcome(err error) bool {
	return err != nil && (isTransientSCFProviderError(err) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF))
}

func isFencedCollectorNode(node *store.CloudNode, pkg *store.FunctionPackage) bool {
	if requiresCollectorPublishLease(pkg) {
		return true
	}
	if node == nil {
		return false
	}
	metadata := parseJSONMap(node.Metadata)
	if metadata["collector_publish_fenced"] == true || strings.EqualFold(metadataString(metadata, "biz_type"), "market_fetcher") {
		return true
	}
	return isMarketFetcherFunction(firstString(node.FunctionName, node.NodeID), node.SpaceID, nil)
}

func validateCollectorPublishLeaseAtSubmit(pkg *store.FunctionPackage, leaseID string, token int64) *pb.RetInfo {
	if requiresCollectorPublishLease(pkg) && !collectorPublishLeaseFieldsPresent(leaseID, token) {
		return retErr(pb.ErrorCode_NO_PERMISSION, "collector market_fetcher SCF mutation requires a publish lease and fencing token")
	}
	return nil
}
