package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultSCFTimeoutSeconds      = 120
	scfOperationTimeout           = 5 * time.Minute
	scfCreateAttemptTimeout       = 4 * time.Minute
	scfCreateReconcileTimeout     = 10 * time.Second
	timerReadbackInterval         = 5 * time.Minute
	timerReadbackBatchSize        = 4
	timerReadbackMinRequestBudget = time.Minute
)

func (s *Service) GetNodeList(ctx context.Context, req *pb.GetNodeListReq) (*pb.GetNodeListRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetNodeListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	nodes, total, err := s.catalog.ListNodes(ctx, spaceID, req)
	if err != nil {
		return &pb.GetNodeListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	// Tencent API has a per-account request rate limit. GetNodeList is called
	// by Collector every few seconds, so refreshing timer nodes in the listing
	// path would turn a read-only snapshot into a blocking SCF API call. The
	// Collector only needs the catalog identity here; runtime metadata is
	// refreshed by CloudNode's write path and by an unbounded admin listing.
	type timerRefreshCandidate struct {
		index int
		at    time.Time
	}
	candidates := make([]timerRefreshCandidate, 0)
	if shouldRefreshTimerReadback(ctx, req) {
		now := time.Now().UTC()
		for index := range nodes {
			if nodes[index].TriggerType != "timer" {
				continue
			}
			last, _ := time.Parse(time.RFC3339Nano, metadataString(parseJSONMap(nodes[index].Metadata), "timer_last_readback_at"))
			if !last.IsZero() && now.Sub(last) < timerReadbackInterval {
				continue
			}
			candidates = append(candidates, timerRefreshCandidate{index: index, at: last})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].at.IsZero() != candidates[j].at.IsZero() {
				return candidates[i].at.IsZero()
			}
			if candidates[i].at.Equal(candidates[j].at) {
				return candidates[i].index < candidates[j].index
			}
			return candidates[i].at.Before(candidates[j].at)
		})
		if len(candidates) > timerReadbackBatchSize {
			candidates = candidates[:timerReadbackBatchSize]
		}
	}
	var refreshWG sync.WaitGroup
	refreshSlots := make(chan struct{}, timerReadbackBatchSize)
	for _, candidate := range candidates {
		index := candidate.index
		refreshWG.Add(1)
		go func(index int) {
			defer refreshWG.Done()
			select {
			case refreshSlots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-refreshSlots }()
			// Refresh the provider readback on every timer-fleet listing. This is
			// a read-only health check, not an Ensure call, so manual Tencent-side
			// trigger drift is visible even when the assignment hash is unchanged.
			// The bounded slots avoid making a 50-node fleet serial or creating an
			// unbounded Tencent API burst.
			if err := s.refreshTimerTriggerMetadata(ctx, spaceID, &nodes[index]); err != nil {
				log.WarnContextf(ctx, "[CloudNode-TencentSCF] timer trigger readback failed node=%s: %v", nodes[index].NodeID, err)
			}
		}(index)
	}
	refreshWG.Wait()
	out := make([]*pb.CloudNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, toPBNode(node))
	}
	page, size := pageFromCommon(req.GetPage())
	return &pb.GetNodeListRsp{
		RetInfo: retOK(),
		Items:   out,
		Page:    pageResult(page, size, total),
	}, nil
}

func shouldRefreshTimerReadback(ctx context.Context, req *pb.GetNodeListReq) bool {
	// Collector reconciliation is a control-plane liveness path. It must not
	// wait for Tencent readback of up to four functions before receiving the
	// catalog snapshot. The normal admin listing remains the readback path.
	if req != nil && strings.EqualFold(strings.TrimSpace(req.GetBizType()), "market_fetcher") {
		return false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timerReadbackMinRequestBudget {
		return false
	}
	return true
}

func (s *Service) refreshTimerTriggerMetadata(ctx context.Context, spaceID string, node *store.CloudNode) error {
	if s == nil || node == nil || node.TriggerType != "timer" {
		return nil
	}
	metadata := parseJSONMap(node.Metadata)
	removeDeprecatedNodeMetadata(metadata)
	readbackAt := time.Now().UTC().Format(time.RFC3339Nano)
	patch := map[string]any{"timer_available_status": "Unknown", "timer_actual_type": nil, "timer_actual_enabled": nil, "timer_actual_cron": nil, "timer_actual_qualifier": nil, "timer_actual_message": nil, "timer_last_readback_at": readbackAt}
	if strings.TrimSpace(node.PackageID) == "" || (strings.TrimSpace(node.DeploymentID) == "" && !metadataBool(metadata, "deployment_ready")) {
		patch["timer_available_status"] = "Unknown"
		err := s.catalog.UpdateNodeRuntimeMetadata(ctx, spaceID, node.NodeID, patch)
		for key, value := range patch {
			if value == nil {
				delete(metadata, key)
				continue
			}
			metadata[key] = value
		}
		node.Metadata = jsonString(metadata)
		return err
	}
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil || account == nil {
		if err == nil {
			err = fmt.Errorf("cloud account unavailable")
		}
		patch["timer_status_error"] = err.Error()
	} else {
		client, clientErr := s.scfClient(ctx, *account)
		if clientErr != nil {
			err = clientErr
			patch["timer_status_error"] = clientErr.Error()
		} else {
			ref := tencentscf.FunctionRef{Region: node.Region, FunctionName: firstString(node.FunctionName, node.NodeID), Namespace: firstString(node.Namespace, "default")}
			var function *tencentscf.FunctionInfo
			var functionErr error
			var info *tencentscf.TimerTriggerInfo
			var lookupErr error
			var readbackWG sync.WaitGroup
			readbackWG.Add(2)
			go func() {
				defer readbackWG.Done()
				function, functionErr = client.GetFunction(ctx, ref)
			}()
			go func() {
				defer readbackWG.Done()
				info, lookupErr = client.GetTimerTrigger(ctx, ref, timerTriggerName)
			}()
			readbackWG.Wait()
			if functionErr != nil || function == nil {
				if functionErr == nil {
					functionErr = fmt.Errorf("empty SCF function readback")
				}
				err = functionErr
				patch["timer_status_error"] = functionErr.Error()
			} else {
				patch["managed_environment_budget_bytes"] = scfManagedEnvironmentBudget(function.Environment)
				if lookupErr != nil {
					err = lookupErr
					patch["timer_status_error"] = lookupErr.Error()
				} else if info == nil {
					patch["timer_available_status"] = "Missing"
					patch["timer_status_error"] = "timer trigger is missing"
				} else {
					patch["timer_available_status"] = firstString(info.AvailableStatus, "Unknown")
					patch["timer_actual_type"] = info.Type
					patch["timer_actual_enabled"] = info.Enabled
					patch["timer_actual_cron"] = info.Cron
					patch["timer_actual_qualifier"] = info.Qualifier
					patch["timer_actual_message"] = info.Message
					patch["timer_status_error"] = nil
				}
			}
		}
	}
	if err != nil && patch["timer_available_status"] == "Unknown" {
		// Keep Unknown rather than preserving an old Available value.
		patch["timer_available_status"] = "Unknown"
	}
	updateErr := s.catalog.UpdateNodeRuntimeMetadata(ctx, spaceID, node.NodeID, patch)
	for key, value := range patch {
		if value == nil {
			delete(metadata, key)
			continue
		}
		metadata[key] = value
	}
	node.Metadata = jsonString(metadata)
	if updateErr != nil {
		return updateErr
	}
	return err
}

func (s *Service) UpdateNode(ctx context.Context, req *pb.UpdateNodeReq) (*pb.UpdateNodeRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	pbNode := req.GetNode()
	if pbNode == nil || pbNode.GetNodeId() == "" {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node.node_id is required")}, nil
	}
	unlockNode := lockSCFNode(spaceID, pbNode.GetNodeId())
	defer unlockNode()
	existing, err := s.catalog.GetNode(ctx, spaceID, pbNode.GetNodeId())
	if err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	var existingPackage *store.FunctionPackage
	if existing != nil && strings.TrimSpace(existing.PackageID) != "" {
		existingPackage, err = s.catalog.GetPackage(ctx, spaceID, existing.PackageID)
		if err != nil {
			return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
	}
	if isFencedCollectorNode(existing, existingPackage) {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_NO_PERMISSION, "fenced Collector nodes must be changed through a publish-fenced operation")}, nil
	}
	requestedNodeType := pbNode.GetNodeType()
	if requestedNodeType == "" && existing != nil {
		requestedNodeType = existing.NodeType
	}
	if strings.TrimSpace(pbNode.GetTriggerType()) != "" {
		if err := validateTriggerType(requestedNodeType, pbNode.GetTriggerType()); err != nil {
			return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
		if existing != nil && normalizeTriggerType(requestedNodeType, pbNode.GetTriggerType()) != existing.TriggerType {
			return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "trigger_type cannot be changed after node creation; recreate the node")}, nil
		}
	}
	node := fromPBNode(spaceID, pbNode)
	if existing != nil {
		node = mergeNodeUpdate(*existing, pbNode)
	}
	var requestedPackage *store.FunctionPackage
	if strings.TrimSpace(node.PackageID) != "" {
		if existingPackage != nil && existing.PackageID == node.PackageID {
			requestedPackage = existingPackage
		} else {
			requestedPackage, err = s.catalog.GetPackage(ctx, spaceID, node.PackageID)
			if err != nil {
				return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
			}
		}
	}
	if isFencedCollectorNode(&node, requestedPackage) {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_NO_PERMISSION, "fenced Collector nodes must be changed through a publish-fenced operation")}, nil
	}
	if err := validateTriggerType(node.NodeType, node.TriggerType); err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.catalog.UpsertNode(ctx, node); err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.UpdateNodeRsp{RetInfo: retOK()}, nil
}

func (s *Service) executeCreateNodeItem(
	ctx context.Context,
	spaceID string,
	item *pb.NodeCreateItem,
	index int,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("node item is required")
	}
	if strings.TrimSpace(item.GetCreateReservationId()) == "" {
		item.CreateReservationId = uuid.NewString()
	}
	node := cloudNodeFromCreateItem(spaceID, item, index)
	node.LifecycleID = item.GetCreateReservationId()
	unlockNode := lockSCFNode(spaceID, node.NodeID)
	defer unlockNode()
	pkg, err := s.catalog.GetPackage(ctx, spaceID, node.PackageID)
	if err != nil {
		return "", err
	}
	if requiresCollectorPublishLease(pkg) {
		metadata := parseJSONMap(node.Metadata)
		metadata["biz_type"] = "market_fetcher"
		metadata["collector_publish_fenced"] = true
		node.Metadata = jsonString(metadata)
	}
	if err := validateTriggerType(node.NodeType, item.GetTriggerType()); err != nil {
		return "", err
	}
	if strings.TrimSpace(node.CloudAccountID) == "" {
		return "", fmt.Errorf("cloud_account_id is required")
	}
	if strings.TrimSpace(node.Region) == "" {
		return "", fmt.Errorf("region is required")
	}
	if strings.TrimSpace(node.PackageID) == "" {
		return "", fmt.Errorf("package_id is required")
	}
	metadata := parseJSONMap(node.Metadata)
	metadata["deployment_ready"] = false
	metadata["create_reservation_id"] = item.GetCreateReservationId()
	node.Metadata = jsonString(metadata)
	if err := s.catalog.ReserveNodeCreate(ctx, node, item.GetCreateReservationId()); err != nil {
		return "", fmt.Errorf("persist SCF create reservation: %w", err)
	}
	if err := s.ensureSCFFunction(ctx, &node, item); err != nil {
		return "", err
	}
	metadata = parseJSONMap(node.Metadata)
	metadata["deployment_ready"] = true
	node.Metadata = jsonString(metadata)
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpdateNodeCreateReservation(persistCtx, node, item.GetCreateReservationId()); err != nil {
		return "", err
	}
	return fmt.Sprintf("created function %s", node.FunctionName), nil
}

func modernMarketFetchNode(node *store.CloudNode, pkg store.FunctionPackage) bool {
	return strings.EqualFold(strings.TrimSpace(pkg.WorkloadType), "market_fetcher") && metadataString(parseJSONMap(node.Metadata), "function_mode") != "instrument_snapshot"
}

func durableMarketFetchTimer(node *store.CloudNode, pkg store.FunctionPackage) bool {
	return modernMarketFetchNode(node, pkg) && node.TriggerType == "timer"
}

func validateMarketFetchTimerTimeout(node *store.CloudNode, pkg store.FunctionPackage, environment map[string]string, outerTimeout int64) error {
	if !durableMarketFetchTimer(node, pkg) {
		return nil
	}
	runtimeValue := strings.TrimSpace(environment["MOOX_FETCH_TIMEOUT_SECONDS"])
	if runtimeValue == "" {
		runtimeValue = strconv.Itoa(tencent.CollectorTimerTimeoutSeconds)
	}
	runtimeTimeout, err := strconv.ParseInt(runtimeValue, 10, 64)
	if err != nil || runtimeTimeout != tencent.CollectorTimerTimeoutSeconds || outerTimeout != tencent.CollectorTimerTimeoutSeconds {
		return fmt.Errorf("Timer runtime timeout MOOX_FETCH_TIMEOUT_SECONDS and config.timeout must both equal %d", tencent.CollectorTimerTimeoutSeconds)
	}
	return nil
}

func validateMarketFetchInvokeTimeout(environment, config map[string]string, outerTimeout int64) error {
	if value, ok := config["timeout"]; ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("Invoke runtime timeout requires a positive config.timeout")
		}
		outerTimeout = parsed
	}
	// Match executionContext's envInt(primary, envInt(alias, 60)) fallback.
	runtimeTimeout := int64(tencent.CollectorTimerTimeoutSeconds)
	for _, key := range []string{"MOOX_FETCH_TIMEOUT_SECONDS", "MOOX_MARKET_FETCH_TIMEOUT_SECONDS"} {
		if value, ok := environment[key]; ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				continue
			}
			runtimeTimeout = int64(parsed)
			break
		}
	}
	if outerTimeout <= 0 || runtimeTimeout != outerTimeout {
		return fmt.Errorf("Invoke runtime timeout must equal effective config.timeout")
	}
	return nil
}

func validateMarketFetchInvokeReadback(node *store.CloudNode, pkg store.FunctionPackage, info *tencentscf.FunctionInfo) error {
	if !modernMarketFetchNode(node, pkg) || node.TriggerType == "timer" {
		return nil
	}
	if info == nil {
		return fmt.Errorf("Invoke runtime timeout configuration readback is empty")
	}
	return validateMarketFetchInvokeTimeout(info.Environment, nil, info.Timeout)
}

func (s *Service) BatchDeleteNodes(ctx context.Context, req *pb.BatchDeleteNodesReq) (*pb.BatchDeleteNodesRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	nodeIDs := compactStrings(req.GetNodeIds())
	if len(nodeIDs) == 0 {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_ids is required")}, nil
	}
	// Stable lock ordering serializes this catalog-only path with local
	// create/deploy operations. The repository transaction below also checks
	// exact row generations so a different process cannot invalidate preflight.
	unlockNodes := lockSCFNodes(spaceID, nodeIDs)
	defer unlockNodes()
	deleteErr := s.catalog.DeleteNodesWithPreflight(ctx, spaceID, nodeIDs, func(node store.CloudNode, pkg *store.FunctionPackage) error {
		if isFencedCollectorNode(&node, pkg) {
			return errFencedCollectorCatalogDelete
		}
		return nil
	})
	if errors.Is(deleteErr, errFencedCollectorCatalogDelete) {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_NO_PERMISSION, "fenced Collector nodes must be deleted through SubmitDeleteNodes")}, nil
	}
	if deleteErr != nil {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, deleteErr.Error())}, nil
	}
	return &pb.BatchDeleteNodesRsp{
		RetInfo:        retOK(),
		ProcessedCount: int32(len(nodeIDs)),
	}, nil
}

var errFencedCollectorCatalogDelete = errors.New("fenced Collector node requires asynchronous deletion")
var errNodeMutationReconciliationRequired = errors.New("node mutation requires reconciliation")

type nodeBatchDurableOperationContextKey struct{}

func withNodeBatchDurableOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, nodeBatchDurableOperationContextKey{}, true)
}

func isNodeBatchDurableOperation(ctx context.Context) bool {
	durable, _ := ctx.Value(nodeBatchDurableOperationContextKey{}).(bool)
	return durable
}

func (s *Service) executeDeleteNodeItem(ctx context.Context, spaceID string, item *pb.NodeDeleteItem) (string, error) {
	if item == nil || strings.TrimSpace(item.GetNodeId()) == "" {
		return "", fmt.Errorf("node_id is required")
	}
	if strings.TrimSpace(item.GetLifecycleId()) == "" || strings.TrimSpace(item.GetOperationId()) == "" {
		return "", fmt.Errorf("delete item lifecycle identity and operation id are required")
	}
	// Lock before reloading; an old durable delete must not observe a stale row,
	// then race with same-ID recreation before it reaches the provider.
	unlockNode := lockSCFNode(spaceID, item.GetNodeId())
	defer unlockNode()
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil {
		return "", err
	}
	if node == nil || node.IsDeleted {
		node, err = s.catalog.GetNodeIncludingDeleted(ctx, spaceID, item.GetNodeId())
		if err != nil {
			return "", err
		}
		if node == nil {
			if collectorPublishLeaseFieldsPresent(item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()) {
				return fmt.Sprintf("node %s was already absent when the fenced delete ran", item.GetNodeId()), nil
			}
			return "", fmt.Errorf("node not found: %s", item.GetNodeId())
		}
	}
	if node.LifecycleID == "" || node.LifecycleID != item.GetLifecycleId() {
		return "", store.ErrNodeLifecycleMismatch
	}
	if err := s.catalog.ClaimNodeDelete(ctx, spaceID, node.NodeID, item.GetLifecycleId(), item.GetOperationId()); err != nil {
		return "", err
	}
	retainClaim := isNodeBatchDurableOperation(ctx)
	defer func() {
		if !retainClaim {
			_ = s.catalog.ReleaseNodeDeleteClaim(context.WithoutCancel(ctx), spaceID, node.NodeID, item.GetLifecycleId(), item.GetOperationId())
		}
	}()
	if node.IsDeleted {
		return fmt.Sprintf("node %s was already deleted", item.GetNodeId()), nil
	}
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil || account == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("cloud account unavailable for node %s", node.NodeID)
	}
	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return "", err
	}
	ref := tencentscf.FunctionRef{Region: node.Region, FunctionName: firstString(node.FunctionName, node.NodeID), Namespace: firstString(node.Namespace, "default")}
	unlockFunction := lockSCFFunction(ref)
	defer unlockFunction()
	deleteRemote := true
	metadata := parseJSONMap(node.Metadata)
	deploymentReady, readinessMarkerPresent := metadata["deployment_ready"].(bool)
	reservation := readinessMarkerPresent && !deploymentReady && strings.TrimSpace(node.DeploymentID) == ""
	if reservation {
		info, readErr := client.GetFunction(ctx, ref)
		if isSCFNotFound(readErr) {
			deleteRemote = false
		} else if readErr != nil {
			return "", fmt.Errorf("verify SCF create reservation ownership for %s: %w", node.NodeID, readErr)
		} else if info == nil {
			return "", fmt.Errorf("verify SCF create reservation ownership for %s: empty function readback", node.NodeID)
		} else if strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"]) != strings.TrimSpace(node.PackageID) ||
			strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"]) != strings.TrimSpace(node.LifecycleID) {
			deleteRemote = false
		}
	}
	if deleteRemote {
		if node.TriggerType == "timer" {
			if err := client.DeleteTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Qualifier: timerTriggerQualifier}); err != nil && !isSCFNotFound(err) {
				retainClaim = retainClaim || isAmbiguousSCFProviderOutcome(err)
				return "", fmt.Errorf("delete timer trigger for %s: %w", node.NodeID, err)
			}
			retainClaim = true
		}
		if err := client.DeleteFunction(ctx, ref); err != nil && !isSCFNotFound(err) {
			retainClaim = retainClaim || isAmbiguousSCFProviderOutcome(err)
			return "", fmt.Errorf("delete scf function %s: %w", node.NodeID, err)
		}
		if err := waitForSCFFunctionDeleted(ctx, client, ref); err != nil {
			retainClaim = true
			return "", fmt.Errorf("%w: verify scf function %s deletion: %w", errNodeMutationReconciliationRequired, ref.FunctionName, err)
		}
	}
	if err := s.catalog.CompleteNodeDelete(ctx, spaceID, node.NodeID, item.GetLifecycleId(), item.GetOperationId()); err != nil {
		if deleteRemote {
			retainClaim = true
			return "", fmt.Errorf("%w: complete catalog deletion after provider delete: %v", errNodeMutationReconciliationRequired, err)
		}
		return "", err
	}
	if !isNodeBatchDurableOperation(ctx) {
		retainClaim = false
	}
	if reservation && !deleteRemote {
		return fmt.Sprintf("removed SCF create reservation %s without deleting an unverified function", ref.FunctionName), nil
	}
	return fmt.Sprintf("deleted function %s", ref.FunctionName), nil
}

func (s *Service) executeDeployNodeItem(
	ctx context.Context,
	spaceID string,
	item *pb.NodeDeployItem,
) (string, error) {
	if item == nil || strings.TrimSpace(item.GetNodeId()) == "" || strings.TrimSpace(item.GetPackageId()) == "" {
		return "", fmt.Errorf("node_id and package_id are required")
	}
	unlockNode := lockSCFNode(spaceID, item.GetNodeId())
	defer unlockNode()
	// Reload only after acquiring the node lock: synchronous catalog deletion and
	// same-process deploys must agree on the exact row that is about to be used.
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil {
		return "", err
	}
	if node == nil || node.IsDeleted {
		return "", fmt.Errorf("node not found: %s", item.GetNodeId())
	}
	lifecycleID, err := s.catalog.EnsureNodeLifecycleID(ctx, spaceID, node.NodeID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(item.GetLifecycleId()) != "" && item.GetLifecycleId() != lifecycleID {
		return "", store.ErrNodeLifecycleMismatch
	}
	node.LifecycleID = lifecycleID
	if strings.TrimSpace(item.GetOperationId()) == "" {
		item.OperationId = uuid.NewString()
	}
	if err := s.catalog.ClaimNodeMutation(ctx, spaceID, node.NodeID, lifecycleID, item.GetOperationId()); err != nil {
		return "", err
	}
	retainClaim := isNodeBatchDurableOperation(ctx)
	defer func() {
		if !retainClaim {
			_ = s.catalog.ReleaseNodeMutationClaim(context.WithoutCancel(ctx), spaceID, node.NodeID, lifecycleID, item.GetOperationId())
		}
	}()
	pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId())
	if err != nil {
		return "", err
	}
	if pkg == nil {
		return "", fmt.Errorf("package not found: %s", item.GetPackageId())
	}
	if pkg.Status != "available" {
		return "", fmt.Errorf("package %s is not available", pkg.PackageID)
	}
	if err := s.updateSCFFunctionCode(ctx, *node, *pkg, item.GetEnvironment(), item.GetConfig()); err != nil {
		if errors.Is(err, errNodeMutationReconciliationRequired) || isAmbiguousSCFProviderOutcome(err) {
			retainClaim = true
			return "", fmt.Errorf("%w: deploy %s: %w", errNodeMutationReconciliationRequired, node.NodeID, err)
		}
		return "", err
	}
	retainClaim = true
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpdateNodeDeployment(persistCtx, spaceID, item.GetNodeId(), item.GetPackageId(), pkg.Version, item.GetConfig(), item.GetEnvironment(), isFencedCollectorNode(node, pkg), lifecycleID, item.GetOperationId()); err != nil {
		retainClaim = true
		return "", fmt.Errorf("%w: persist deployment for %s: %v", errNodeMutationReconciliationRequired, node.NodeID, err)
	}
	// The durable runner releases the claim with the terminal item transition.
	if !isNodeBatchDurableOperation(ctx) {
		retainClaim = false
	}
	return fmt.Sprintf("deployed package %s to %s", pkg.PackageID, node.NodeID), nil
}

func (s *Service) ensureSCFFunction(ctx context.Context, node *store.CloudNode, item *pb.NodeCreateItem) error {
	if strings.EqualFold(strings.TrimSpace(node.Region), "local") {
		return s.ensureLocalPackage(ctx, node, item)
	}
	pkg, account, err := s.packageAndAccount(ctx, node.SpaceID, node.PackageID, node.CloudAccountID)
	if err != nil {
		return err
	}
	if pkg.Status != "available" {
		return fmt.Errorf("package %s is not available", pkg.PackageID)
	}
	reservationID := strings.TrimSpace(item.GetCreateReservationId())
	if reservationID == "" {
		return fmt.Errorf("create reservation identity is required")
	}
	node.PackageVersion = pkg.Version
	metadata := parseJSONMap(node.Metadata)
	config := item.GetConfig()
	memorySize := configInt64(config, "memory_size", 256)
	timeoutSeconds := configInt64(config, "timeout", defaultSCFTimeoutSeconds)
	effectiveConfig := config
	if durableMarketFetchTimer(node, *pkg) {
		timeoutSeconds = configInt64(config, "timeout", tencent.CollectorTimerTimeoutSeconds)
		if err := validateMarketFetchTimerTimeout(node, *pkg, item.GetEnvironment(), timeoutSeconds); err != nil {
			return err
		}
		effectiveConfig = copyStringMap(config)
		if effectiveConfig == nil {
			effectiveConfig = make(map[string]string)
		}
		effectiveConfig["timeout"] = strconv.FormatInt(timeoutSeconds, 10)
		environment := copyStringMap(item.GetEnvironment())
		if environment == nil {
			environment = make(map[string]string)
		}
		environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
		if err := tencent.ValidateCollectorTimerEnvironment(environment); err != nil {
			return err
		}
	} else if modernMarketFetchNode(node, *pkg) {
		if err := validateMarketFetchInvokeTimeout(item.GetEnvironment(), config, timeoutSeconds); err != nil {
			return err
		}
		environment := copyStringMap(item.GetEnvironment())
		if environment == nil {
			environment = make(map[string]string)
		}
		environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
		if err := tencent.ValidateCollectorMarketFetchEnvironment(environment); err != nil {
			return err
		}
	}
	// Keep the submitted Timer timeout authoritative from initial creation.
	if isMarketFetchNode(node) && node.TriggerType == "timer" {
		memorySize = 64
	}
	metadata["runtime"] = firstString(item.GetRuntime(), pkg.Runtime, metadataString(metadata, "runtime"))
	metadata["handler"] = firstString(item.GetHandler(), metadataString(metadata, "handler"), "main")
	node.Metadata = jsonString(metadata)

	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return err
	}
	ref := tencentscf.FunctionRef{
		Region:       node.Region,
		FunctionName: firstString(node.FunctionName, node.NodeID),
		Namespace:    firstString(node.Namespace, "default"),
	}
	if err := client.EnsureNamespace(ctx, ref.Region, ref.Namespace); err != nil {
		return fmt.Errorf("ensure SCF namespace %s in %s: %w", ref.Namespace, ref.Region, err)
	}
	unlockFunction := lockSCFFunction(ref)
	defer unlockFunction()
	info, err := client.GetFunction(ctx, ref)
	if err == nil {
		remotePackageID := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"])
		if remotePackageID != pkg.PackageID {
			return fmt.Errorf(
				"scf function %s already exists with code package %q; expected %q",
				ref.FunctionName,
				remotePackageID,
				pkg.PackageID,
			)
		}
		remoteReservationID := strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"])
		if remoteReservationID != reservationID {
			return fmt.Errorf(
				"scf function %s already exists with create reservation %q; expected %q",
				ref.FunctionName,
				remoteReservationID,
				reservationID,
			)
		}
		info, err = waitForSCFActive(ctx, client, ref, info)
		if err != nil {
			return err
		}
		if err := reconcileSCFPublicNetwork(ctx, client, ref, info, effectiveConfig); err != nil {
			return err
		}
		if err := validateMarketFetchInvokeReadback(node, *pkg, info); err != nil {
			return err
		}
		if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
			return err
		}
		if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchNode(node)); err != nil {
			return err
		}
		if err := ensureInitialTimerTrigger(ctx, client, node, ref); err != nil {
			return err
		}
		// A previous attempt may have created the function before CloudNode was
		// interrupted. The reservation nonce proves ownership of this request.
		return nil
	}
	if !isSCFNotFound(err) {
		return fmt.Errorf("get scf function %s: %w", ref.FunctionName, err)
	}
	environment := copyStringMap(item.GetEnvironment())
	if environment == nil {
		environment = make(map[string]string)
	}
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	environment["MOOX_CREATE_RESERVATION_ID"] = reservationID
	if err := tencent.ValidateSCFEnvironment(environment); err != nil {
		return fmt.Errorf("scf function %s %w", ref.FunctionName, err)
	}
	createCtx, createCancel := context.WithTimeout(ctx, scfCreateAttemptTimeout)
	_, err = client.CreateFunction(createCtx, tencentscf.CreateFunctionRequest{
		FunctionRef:            ref,
		Runtime:                firstString(item.GetRuntime(), pkg.Runtime, "CustomRuntime"),
		Handler:                firstString(item.GetHandler(), "main"),
		Description:            fmt.Sprintf("MooX cloud function node %s", node.NodeID),
		MemorySize:             memorySize,
		Timeout:                timeoutSeconds,
		MaxInstanceConcurrency: configInt64(effectiveConfig, "max_instance_concurrency", 0),
		Environment:            environment,
		COSBucket:              pkg.COSBucket,
		COSRegion:              firstString(pkg.COSRegion, account.COSRegion),
		COSObject:              strings.TrimPrefix(pkg.COSPath, "/"),
		Type:                   firstString(config["function_type"], "Event"),
		PublicNetStatus:        desiredSCFPublicNetStatus(effectiveConfig),
		VpcID:                  strings.TrimSpace(effectiveConfig["vpc_id"]),
		SubnetID:               strings.TrimSpace(effectiveConfig["subnet_id"]),
	})
	createCancel()
	if err != nil {
		createErr := err
		if errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("create scf function %s: %w", ref.FunctionName, createErr)
		}
		reconcileCtx := ctx
		reconcileCancel := func() {}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reconcileCtx, reconcileCancel = context.WithTimeout(
				context.WithoutCancel(ctx),
				scfCreateReconcileTimeout,
			)
		}
		defer reconcileCancel()
		info, err = client.GetFunction(reconcileCtx, ref)
		if err != nil {
			return fmt.Errorf("create scf function %s: %w", ref.FunctionName, createErr)
		}
		remotePackageID := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"])
		remoteReservationID := strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"])
		if remotePackageID != pkg.PackageID || remoteReservationID != reservationID {
			return fmt.Errorf(
				"create scf function %s returned an ambiguous error and remote reservation identity/package is %q/%q, expected %q/%q: %w",
				ref.FunctionName,
				remoteReservationID,
				remotePackageID,
				reservationID,
				pkg.PackageID,
				createErr,
			)
		}
		info, err = waitForSCFActive(reconcileCtx, client, ref, info)
		if err != nil {
			return err
		}
		if err := reconcileSCFPublicNetwork(reconcileCtx, client, ref, info, effectiveConfig); err != nil {
			return err
		}
		if err := validateMarketFetchInvokeReadback(node, *pkg, info); err != nil {
			return err
		}
		if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
			return err
		}
		if err := ensureSCFAsyncRetryConfig(reconcileCtx, client, ref, isMarketFetchNode(node)); err != nil {
			return err
		}
		if err := ensureInitialTimerTrigger(reconcileCtx, client, node, ref); err != nil {
			return err
		}
		return nil
	}
	info, err = waitForSCFActive(ctx, client, ref, nil)
	if err != nil {
		return err
	}
	if err := reconcileSCFPublicNetwork(ctx, client, ref, info, effectiveConfig); err != nil {
		return err
	}
	if err := validateMarketFetchInvokeReadback(node, *pkg, info); err != nil {
		return err
	}
	if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
		return err
	}
	if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchNode(node)); err != nil {
		return err
	}
	if err := ensureInitialTimerTrigger(ctx, client, node, ref); err != nil {
		return err
	}
	return nil
}

func ensureInitialTimerTrigger(ctx context.Context, client scfProvisioner, node *store.CloudNode, ref tencentscf.FunctionRef) error {
	if node == nil || node.TriggerType != "timer" {
		return nil
	}
	_, err := client.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Cron: "0 * * * * * *", Enabled: false, Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	if err != nil {
		return fmt.Errorf("ensure initial timer trigger for %s: %w", ref.FunctionName, err)
	}
	return nil
}

func ensureSCFAsyncRetryConfig(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, enabled bool) error {
	if !enabled {
		return nil
	}
	configurer, ok := client.(interface {
		UpdateFunctionEventInvokeConfig(context.Context, tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error)
	})
	if !ok {
		return nil
	}
	// Tencent requires a minimum 60-second async retention period. Keep it
	// close to the Collector deadline; retries are owned by MooX, not SCF.
	if _, err := configurer.UpdateFunctionEventInvokeConfig(ctx, tencentscf.UpdateFunctionEventInvokeConfigRequest{FunctionRef: ref, RetryNum: 0, MsgTTL: 60}); err != nil {
		return fmt.Errorf("configure scf async retry for %s: %w", ref.FunctionName, err)
	}
	return nil
}

// ensureLocalPackage lets local-region nodes (used by E2E tests) run without
// Tencent credentials or COS: they need only an available package descriptor,
// not a remote SCF function.
func (s *Service) ensureLocalPackage(ctx context.Context, node *store.CloudNode, item *pb.NodeCreateItem) error {
	pkg, err := s.catalog.GetPackage(ctx, node.SpaceID, node.PackageID)
	if err != nil {
		return err
	}
	if pkg == nil {
		pkg = &store.FunctionPackage{
			SpaceID:        node.SpaceID,
			PackageID:      node.PackageID,
			PackageName:    node.PackageID,
			Version:        firstString(item.GetRuntime(), "local"),
			Description:    "local runtime package",
			Runtime:        firstString(item.GetRuntime(), "go1"),
			PackageType:    "collector",
			WorkloadType:   "collect.kline",
			OriginalName:   node.PackageID,
			FileSize:       1,
			FileMD5:        "local",
			CloudAccountID: node.CloudAccountID,
			Status:         "available",
		}
	} else {
		pkg.Status = "available"
		if pkg.Version == "" {
			pkg.Version = firstString(item.GetRuntime(), "local")
		}
	}
	if err := s.catalog.UpsertPackage(ctx, *pkg); err != nil {
		return err
	}
	node.PackageVersion = pkg.Version
	return nil
}

func (s *Service) updateSCFFunctionCode(
	ctx context.Context,
	node store.CloudNode,
	pkg store.FunctionPackage,
	desiredEnvironment map[string]string,
	desiredConfig map[string]string,
) error {
	mutationStarted := false
	requireReconciliationAfterMutation := func(err error) error {
		if err == nil || !mutationStarted || errors.Is(err, errNodeMutationReconciliationRequired) {
			return err
		}
		return fmt.Errorf("%w: %w", errNodeMutationReconciliationRequired, err)
	}
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("cloud account not found: %s", node.CloudAccountID)
	}
	if !isTencentProvider(account.Provider) {
		return fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	metadata := parseJSONMap(node.Metadata)
	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return err
	}
	ref := tencentscf.FunctionRef{
		Region:       node.Region,
		FunctionName: firstString(node.FunctionName, node.NodeID),
		Namespace:    firstString(node.Namespace, "default"),
	}
	unlock := lockSCFFunction(ref)
	defer unlock()
	info, err := client.GetFunction(ctx, ref)
	if err != nil {
		return fmt.Errorf("get scf function %s before deploy: %w", ref.FunctionName, err)
	}
	// MOOX_CODE_PACKAGE_ID is written only after the code update has completed
	// and the desired configuration update has been accepted. It therefore
	// doubles as the idempotency marker when the caller times out while Tencent
	// is still returning Updating.
	codeCurrent := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"]) == pkg.PackageID
	info, err = waitForSCFActive(ctx, client, ref, info)
	if err != nil {
		return err
	}
	// Deployment callers own only the base runtime variables. Preserve the
	// Collector-managed timer assignment when a function is republished; the
	// next reconciliation will replace it if the desired assignment changed.
	// Validate the complete merged environment before uploading code so an
	// oversized timer deployment cannot leave a new package with old config.
	environment := copyStringMap(info.Environment)
	if environment == nil {
		environment = make(map[string]string)
	}
	for key, value := range desiredEnvironment {
		environment[key] = value
	}
	if modernMarketFetchNode(&node, pkg) {
		tencent.RemoveCollectorInternalGatewayEnvironment(environment)
	}
	if strings.TrimSpace(environment["MOOX_EVENTBUS_NATS_TLS_CA_FILE"]) != "" {
		delete(environment, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	}
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	if err := tencent.ValidateSCFEnvironment(environment); err != nil {
		return fmt.Errorf("scf function %s %w", ref.FunctionName, err)
	}
	if durableMarketFetchTimer(&node, pkg) {
		if err := validateMarketFetchTimerTimeout(&node, pkg, environment, configInt64(desiredConfig, "timeout", info.Timeout)); err != nil {
			return err
		}
		if err := tencent.ValidateCollectorTimerEnvironment(environment); err != nil {
			return err
		}
	} else if modernMarketFetchNode(&node, pkg) {
		if err := validateMarketFetchInvokeTimeout(environment, desiredConfig, info.Timeout); err != nil {
			return err
		}
		if err := tencent.ValidateCollectorMarketFetchEnvironment(environment); err != nil {
			return err
		}
	}
	if !codeCurrent {
		// Treat every error from this point as an uncertain partial deployment:
		// even provider errors that look deterministic may follow a committed mutation.
		mutationStarted = true
		_, err = client.UpdateFunctionCode(ctx, tencentscf.UpdateFunctionCodeRequest{
			FunctionRef: ref,
			Handler:     firstString(metadataString(metadata, "handler"), "main"),
			COSBucket:   pkg.COSBucket,
			COSRegion:   firstString(pkg.COSRegion, account.COSRegion),
			COSObject:   strings.TrimPrefix(pkg.COSPath, "/"),
		})
		if err != nil {
			return requireReconciliationAfterMutation(fmt.Errorf("update scf function %s: %w", firstString(node.FunctionName, node.NodeID), err))
		}
		if _, err := waitForSCFActive(ctx, client, ref, nil); err != nil {
			return requireReconciliationAfterMutation(err)
		}
	}
	mutationStarted = true
	if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{
		FunctionRef:            ref,
		Environment:            environment,
		MemorySize:             configInt64(desiredConfig, "memory_size", 0),
		Timeout:                configInt64(desiredConfig, "timeout", 0),
		MaxInstanceConcurrency: configInt64(desiredConfig, "max_instance_concurrency", 0),
		PublicNetStatus:        desiredSCFPublicNetStatus(desiredConfig),
		VpcID:                  strings.TrimSpace(desiredConfig["vpc_id"]),
		SubnetID:               strings.TrimSpace(desiredConfig["subnet_id"]),
		ClearVPC:               desiredSCFClearVPC(desiredConfig),
		ClearNativeCLS:         true,
	}); err != nil {
		return requireReconciliationAfterMutation(fmt.Errorf("update scf function %s configuration: %w", ref.FunctionName, err))
	}
	if _, err = waitForSCFActive(ctx, client, ref, nil); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	verified, err := client.GetFunction(ctx, ref)
	if err != nil {
		return requireReconciliationAfterMutation(fmt.Errorf("verify scf function %s configuration: %w", ref.FunctionName, err))
	}
	if err := verifySCFFunctionConfiguration(verified, desiredConfig, desiredEnvironment, ref.FunctionName); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	if err := validateMarketFetchInvokeReadback(&node, pkg, verified); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchNode(&node)); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	return requireReconciliationAfterMutation(ensureInitialTimerTrigger(ctx, client, &node, ref))
}

func verifySCFFunctionConfiguration(info *tencentscf.FunctionInfo, desiredConfig, desiredEnvironment map[string]string, functionName string) error {
	if info == nil {
		return fmt.Errorf("scf function %s configuration readback is empty", functionName)
	}
	if expected := configInt64(desiredConfig, "memory_size", 0); expected > 0 {
		if info.MemorySize <= 0 || info.MemorySize != expected {
			return fmt.Errorf("scf function %s memory_size=%d; expected %d", functionName, info.MemorySize, expected)
		}
	}
	if expected := configInt64(desiredConfig, "timeout", 0); expected > 0 {
		if info.Timeout <= 0 || info.Timeout != expected {
			return fmt.Errorf("scf function %s timeout=%d; expected %d", functionName, info.Timeout, expected)
		}
	}
	if expected := configInt64(desiredConfig, "max_instance_concurrency", 0); expected > 0 {
		if info.MaxInstanceConcurrency <= 0 || info.MaxInstanceConcurrency != expected {
			return fmt.Errorf("scf function %s max_instance_concurrency=%d; expected %d (SCF readback unavailable or mismatched)", functionName, info.MaxInstanceConcurrency, expected)
		}
	}
	if expected := desiredSCFPublicNetStatus(desiredConfig); expected != "" && !strings.EqualFold(info.PublicNetStatus, expected) {
		return fmt.Errorf("scf function %s public_net_status=%q; expected %q", functionName, info.PublicNetStatus, expected)
	}
	if expected := strings.TrimSpace(desiredConfig["vpc_id"]); expected != "" && info.VpcID != expected {
		return fmt.Errorf("scf function %s vpc_id=%q; expected %q", functionName, info.VpcID, expected)
	}
	if expected := strings.TrimSpace(desiredConfig["subnet_id"]); expected != "" && info.SubnetID != expected {
		return fmt.Errorf("scf function %s subnet_id=%q; expected %q", functionName, info.SubnetID, expected)
	}
	if desiredSCFClearVPC(desiredConfig) && (strings.TrimSpace(info.VpcID) != "" || strings.TrimSpace(info.SubnetID) != "") {
		return fmt.Errorf("scf function %s remains attached to vpc_id=%q subnet_id=%q; expected unbound", functionName, info.VpcID, info.SubnetID)
	}
	for key, expected := range desiredEnvironment {
		if actual, ok := info.Environment[key]; !ok || actual != expected {
			return fmt.Errorf("scf function %s environment %q does not match desired configuration", functionName, key)
		}
	}
	return nil
}

func desiredSCFPublicNetStatus(config map[string]string) string {
	return strings.ToUpper(strings.TrimSpace(config["public_net_status"]))
}

func desiredSCFClearVPC(config map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(config["clear_vpc"]), "true")
}

func reconcileSCFPublicNetwork(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, info *tencentscf.FunctionInfo, desiredConfig map[string]string) error {
	expected := desiredSCFPublicNetStatus(desiredConfig)
	expectedVPC := strings.TrimSpace(desiredConfig["vpc_id"])
	expectedSubnet := strings.TrimSpace(desiredConfig["subnet_id"])
	clearVPC := desiredSCFClearVPC(desiredConfig)
	publicMismatch := expected != "" && (info == nil || !strings.EqualFold(info.PublicNetStatus, expected))
	vpcMismatch := (expectedVPC != "" && (info == nil || info.VpcID != expectedVPC || info.SubnetID != expectedSubnet)) ||
		(clearVPC && info != nil && (strings.TrimSpace(info.VpcID) != "" || strings.TrimSpace(info.SubnetID) != ""))
	if info == nil || (!publicMismatch && !vpcMismatch) {
		return nil
	}
	if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{
		FunctionRef: ref, Environment: info.Environment, PublicNetStatus: expected,
		VpcID: expectedVPC, SubnetID: expectedSubnet,
		ClearVPC: clearVPC,
	}); err != nil {
		return fmt.Errorf("update scf function %s public network: %w", ref.FunctionName, err)
	}
	updated, err := waitForSCFActive(ctx, client, ref, nil)
	if err != nil {
		return err
	}
	*info = *updated
	return nil
}

func isMarketFetchNode(node *store.CloudNode) bool {
	return node != nil && strings.EqualFold(strings.TrimSpace(metadataString(parseJSONMap(node.Metadata), "biz_type")), "market_fetcher")
}

func acceptedSCFPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.WithTimeout(context.WithoutCancel(ctx), nodeBatchCompletionTimeout)
	}
	return context.WithCancel(ctx)
}

func waitForSCFActive(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, current *tencentscf.FunctionInfo) (*tencentscf.FunctionInfo, error) {
	waitCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
	defer cancel()
	for {
		if current == nil {
			info, err := client.GetFunction(waitCtx, ref)
			if err != nil {
				if !isSCFNotFound(err) && !isTransientSCFProviderError(err) {
					return nil, fmt.Errorf("get scf function %s status: %w", ref.FunctionName, err)
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-waitCtx.Done():
					timer.Stop()
					return nil, fmt.Errorf("wait for scf function %s active: %w", ref.FunctionName, waitCtx.Err())
				case <-timer.C:
					continue
				}
			}
			current = info
		}
		status := strings.ToLower(strings.TrimSpace(current.Status))
		if status == "active" {
			return current, nil
		}
		if status == "" || strings.Contains(status, "failed") {
			return nil, fmt.Errorf("scf function %s entered status %q", ref.FunctionName, current.Status)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for scf function %s active: %w", ref.FunctionName, waitCtx.Err())
		case <-timer.C:
			current = nil
		}
	}
}

func waitForSCFFunctionDeleted(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef) error {
	waitCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
	defer cancel()
	for {
		_, err := client.GetFunction(waitCtx, ref)
		if isSCFNotFound(err) {
			return nil
		}
		if err != nil && !isTransientSCFProviderError(err) {
			return fmt.Errorf("read function after delete: %w", err)
		}
		if waitCtx.Err() != nil {
			return fmt.Errorf("wait for scf function deletion: %w", waitCtx.Err())
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("wait for scf function deletion: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (s *Service) packageAndAccount(ctx context.Context, spaceID string, packageID string, accountID string) (*store.FunctionPackage, *store.CloudAccount, error) {
	pkg, err := s.catalog.GetPackage(ctx, spaceID, packageID)
	if err != nil {
		return nil, nil, err
	}
	if pkg == nil {
		return nil, nil, fmt.Errorf("package not found: %s", packageID)
	}
	account, err := s.catalog.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if account == nil {
		return nil, nil, fmt.Errorf("cloud account not found: %s", accountID)
	}
	if !isTencentProvider(account.Provider) {
		return nil, nil, fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	return pkg, account, nil
}

func (s *Service) scfClient(ctx context.Context, account store.CloudAccount) (scfProvisioner, error) {
	credential, err := s.resolveCloudCredential(ctx, account)
	if err != nil {
		return nil, err
	}
	factory := s.scfClientFactory
	if factory == nil {
		factory = defaultSCFClientFactory
	}
	return factory(credential), nil
}

func (s *Service) resolveCloudCredential(ctx context.Context, account store.CloudAccount) (cloudcredential.TencentCredential, error) {
	if s.credentialResolver == nil {
		return cloudcredential.TencentCredential{}, fmt.Errorf("cloud credential resolver is not configured")
	}
	credential, err := s.credentialResolver.Resolve(ctx, account)
	if err != nil {
		return cloudcredential.TencentCredential{}, err
	}
	return credential, nil
}

func isTencentProvider(provider string) bool {
	return strings.TrimSpace(provider) == "tencent"
}

func normalizeTriggerType(nodeType, triggerType string) string {
	nodeType = strings.TrimSpace(nodeType)
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	if nodeType != "scf-event" {
		return ""
	}
	if triggerType == "timer" {
		return "timer"
	}
	return "invoke"
}

func validateTriggerType(nodeType, triggerType string) error {
	nodeType = strings.TrimSpace(nodeType)
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	if nodeType != "scf-event" {
		if triggerType != "" {
			return fmt.Errorf("trigger_type is only valid for scf-event nodes")
		}
		return nil
	}
	if triggerType != "" && triggerType != "invoke" && triggerType != "timer" {
		return fmt.Errorf("trigger_type must be invoke or timer")
	}
	return nil
}

func isSCFNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "resourcenotfound") || strings.Contains(msg, "not found")
}

func configInt64(values map[string]string, key string, fallback int64) int64 {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloudNodeFromCreateItem(spaceID string, item *pb.NodeCreateItem, index int) store.CloudNode {
	metadata := structMap(item.GetMetadata())
	if _, ok := metadata["index"]; !ok {
		metadata["index"] = index
	}
	if item.GetRuntime() != "" {
		metadata["runtime"] = item.GetRuntime()
	}
	if item.GetHandler() != "" {
		metadata["handler"] = item.GetHandler()
	}
	if len(item.GetConfig()) > 0 {
		metadata["config"] = item.GetConfig()
	}
	prefix := firstString(metadataString(metadata, "function_name_prefix"), "moox-cloudnode")
	indexSuffix := firstString(metadataString(metadata, "index"), strconv.Itoa(index))
	functionName := firstString(
		metadataString(metadata, "function_name"),
		fmt.Sprintf(
			"%s-%s-%s",
			prefix,
			firstString(item.GetRegion(), "region"),
			indexSuffix,
		),
	)
	nodeID := firstString(metadataString(metadata, "node_id"), functionName)
	return store.CloudNode{
		SpaceID:        spaceID,
		NodeID:         nodeID,
		CloudAccountID: item.GetCloudAccountId(),
		PackageID:      item.GetPackageId(),
		DeploymentID:   firstString(item.GetDeploymentId(), metadataString(metadata, "deployment_id")),
		NodeType:       firstString(item.GetNodeType(), "scf-event"),
		TriggerType:    normalizeTriggerType(firstString(item.GetNodeType(), "scf-event"), item.GetTriggerType()),
		Provider:       "tencent-scf",
		Region:         item.GetRegion(),
		Namespace:      firstString(item.GetNamespace(), "default"),
		FunctionName:   functionName,
		Metadata:       jsonString(metadata),
		IsDeleted:      false,
	}
}

func mergeNodeUpdate(existing store.CloudNode, node *pb.CloudNode) store.CloudNode {
	next := existing
	if node.GetCloudAccountId() != "" {
		next.CloudAccountID = node.GetCloudAccountId()
	}
	if node.GetPackageId() != "" {
		next.PackageID = node.GetPackageId()
	}
	if node.GetPackageVersion() != "" {
		next.PackageVersion = node.GetPackageVersion()
	}
	if node.GetDeploymentId() != "" {
		next.DeploymentID = node.GetDeploymentId()
	}
	if node.GetNodeType() != "" {
		next.NodeType = node.GetNodeType()
	}
	if node.GetTriggerType() != "" {
		next.TriggerType = normalizeTriggerType(next.NodeType, node.GetTriggerType())
	}
	if node.GetProvider() != "" {
		next.Provider = node.GetProvider()
	}
	if node.GetRegion() != "" {
		next.Region = node.GetRegion()
	}
	if node.GetNamespace() != "" {
		next.Namespace = node.GetNamespace()
	}
	if node.GetFunctionName() != "" {
		next.FunctionName = node.GetFunctionName()
	}
	metadata := nodeMetadataFromPB(node)
	if len(metadata) > 0 {
		merged := parseJSONMap(mergeMetadataJSON(existing.Metadata, jsonString(metadata)))
		if parseJSONMap(existing.Metadata)["collector_publish_fenced"] == true {
			merged["collector_publish_fenced"] = true
		}
		removeDeprecatedNodeMetadata(merged)
		next.Metadata = jsonString(merged)
	} else {
		merged := parseJSONMap(existing.Metadata)
		removeDeprecatedNodeMetadata(merged)
		next.Metadata = jsonString(merged)
	}
	next.IsDeleted = node.GetIsDeleted()
	return next
}

func toPBNode(node store.CloudNode) *pb.CloudNode {
	metadata := parseJSONMap(node.Metadata)
	st, _ := structpb.NewStruct(metadata)
	if st == nil {
		st = &structpb.Struct{}
	}
	return &pb.CloudNode{
		Id:             int32(node.ID),
		SpaceId:        node.SpaceID,
		NodeId:         node.NodeID,
		CloudAccountId: node.CloudAccountID,
		PackageId:      node.PackageID,
		PackageVersion: node.PackageVersion,
		DeploymentId:   node.DeploymentID,
		Namespace:      node.Namespace,
		NodeType:       node.NodeType,
		TriggerType:    node.TriggerType,
		Provider:       node.Provider,
		FunctionName:   node.FunctionName,
		BizType:        metadataString(metadata, "biz_type"),
		Region:         node.Region,
		Tag:            metadataString(metadata, "tag"),
		IpAddress:      metadataString(metadata, "ip_address"),
		Metadata:       st,
		IsDeleted:      node.IsDeleted,
		CreateTime:     formatTime(node.CreateTime),
		ModifyTime:     formatTime(node.ModifyTime),
	}
}

func fromPBNode(spaceID string, node *pb.CloudNode) store.CloudNode {
	metadata := nodeMetadataFromPB(node)
	return store.CloudNode{
		SpaceID:        spaceID,
		NodeID:         node.GetNodeId(),
		CloudAccountID: node.GetCloudAccountId(),
		PackageID:      node.GetPackageId(),
		PackageVersion: node.GetPackageVersion(),
		DeploymentID:   node.GetDeploymentId(),
		NodeType:       firstString(node.GetNodeType(), "scf-event"),
		TriggerType:    normalizeTriggerType(firstString(node.GetNodeType(), "scf-event"), node.GetTriggerType()),
		Provider:       firstString(node.GetProvider(), "tencent-scf"),
		Region:         node.GetRegion(),
		Namespace:      node.GetNamespace(),
		FunctionName:   firstString(node.GetFunctionName(), metadataString(metadata, "function_name"), node.GetNodeId()),
		Metadata:       jsonString(metadata),
		IsDeleted:      node.GetIsDeleted(),
	}
}

func nodeMetadataFromPB(node *pb.CloudNode) map[string]any {
	metadata := structMap(node.GetMetadata())
	if node == nil {
		return metadata
	}
	if node.GetBizType() != "" {
		metadata["biz_type"] = node.GetBizType()
	}
	if node.GetTag() != "" {
		metadata["tag"] = node.GetTag()
	}
	if node.GetIpAddress() != "" {
		metadata["ip_address"] = node.GetIpAddress()
	}
	return metadata
}

func removeDeprecatedNodeMetadata(metadata map[string]any) {
	delete(metadata, "timeout_threshold")
	delete(metadata, "probe_enabled")
	delete(metadata, "probe_url")
}
