package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultSCFTimeoutSeconds  = 120
	scfOperationTimeout       = 5 * time.Minute
	scfCreateAttemptTimeout   = 4 * time.Minute
	scfCreateReconcileTimeout = 10 * time.Second
	scfWatchdogEnvironmentKey = "MOOX_SCF_WATCHDOG_ENABLED"
	scfWatchdogMetadataKey    = "scf_watchdog_enabled"
)

var scfWatchdogSelectionMu sync.Mutex

func (s *Service) GetNodeList(ctx context.Context, req *pb.GetNodeListReq) (*pb.GetNodeListRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetNodeListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	nodes, total, err := s.catalog.ListNodes(ctx, spaceID, req)
	if err != nil {
		return &pb.GetNodeListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
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

func (s *Service) UpdateNode(ctx context.Context, req *pb.UpdateNodeReq) (*pb.UpdateNodeRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	pbNode := req.GetNode()
	if pbNode == nil || pbNode.GetNodeId() == "" {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node.node_id is required")}, nil
	}
	node := fromPBNode(spaceID, pbNode)
	existing, err := s.catalog.GetNode(ctx, spaceID, pbNode.GetNodeId())
	if err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if existing != nil {
		node = mergeNodeUpdate(*existing, pbNode)
	}
	if err := s.ensureNodeExecutionQueues(ctx, node.SpaceID, parseStringSliceJSON(node.SupportedWorkloads)); err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
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
	node := cloudNodeFromCreateItem(spaceID, item, index)
	watchdogEnabled, _, err := requestedSCFWatchdog(item.GetEnvironment())
	if err != nil {
		return "", err
	}
	if watchdogEnabled && strings.EqualFold(strings.TrimSpace(node.Region), "local") {
		return "", fmt.Errorf("SCF watchdog sentinel requires a cloud SCF node")
	}
	if watchdogEnabled {
		// Node batch items execute concurrently. Serialize the small number of
		// sentinel selections through provider acceptance and catalog persistence
		// so two items in one batch cannot both pass the catalog check.
		scfWatchdogSelectionMu.Lock()
		defer scfWatchdogSelectionMu.Unlock()
		if err := s.ensureSingleSCFWatchdog(ctx, spaceID, node.NodeID); err != nil {
			return "", err
		}
		metadata := parseJSONMap(node.Metadata)
		metadata[scfWatchdogMetadataKey] = true
		node.Metadata = jsonString(metadata)
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
	if err := s.ensureNodeExecutionQueues(ctx, node.SpaceID, parseStringSliceJSON(node.SupportedWorkloads)); err != nil {
		return "", err
	}
	if err := s.ensureSCFFunction(ctx, &node, item); err != nil {
		return "", err
	}
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpsertNode(persistCtx, node); err != nil {
		return "", err
	}
	return fmt.Sprintf("created function %s", node.FunctionName), nil
}

func (s *Service) ensureNodeExecutionQueues(ctx context.Context, spaceID string, workloads []string) error {
	if s == nil || s.executionQueue == nil {
		return nil
	}
	for _, jobType := range compactStrings(workloads) {
		if err := s.executionQueue.EnsureJobExecutionQueue(ctx, cloudjobqueue.Identity{
			SpaceID: spaceID,
			JobType: jobType,
		}); err != nil {
			return fmt.Errorf("ensure execution queue for workload %s: %w", jobType, err)
		}
	}
	return nil
}

func (s *Service) BatchDeleteNodes(ctx context.Context, req *pb.BatchDeleteNodesReq) (*pb.BatchDeleteNodesRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	nodeIDs := compactStrings(req.GetNodeIds())
	if len(nodeIDs) == 0 {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_ids is required")}, nil
	}
	if err := s.catalog.DeleteNodes(ctx, spaceID, nodeIDs); err != nil {
		return &pb.BatchDeleteNodesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.BatchDeleteNodesRsp{
		RetInfo:        retOK(),
		ProcessedCount: int32(len(nodeIDs)),
	}, nil
}

func (s *Service) executeDeployNodeItem(
	ctx context.Context,
	spaceID string,
	item *pb.NodeDeployItem,
) (string, error) {
	if item == nil || strings.TrimSpace(item.GetNodeId()) == "" || strings.TrimSpace(item.GetPackageId()) == "" {
		return "", fmt.Errorf("node_id and package_id are required")
	}
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil {
		return "", err
	}
	if node == nil {
		return "", fmt.Errorf("node not found: %s", item.GetNodeId())
	}
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
	watchdogEnabled := metadataBool(parseJSONMap(node.Metadata), scfWatchdogMetadataKey)
	requestedWatchdog, watchdogSpecified, err := requestedSCFWatchdog(item.GetEnvironment())
	if err != nil {
		return "", err
	}
	if watchdogSpecified && requestedWatchdog != watchdogEnabled {
		return "", fmt.Errorf(
			"SCF watchdog sentinel selection for node %s is immutable; delete and recreate the selected node",
			node.NodeID,
		)
	}
	if err := s.ensureNodeExecutionQueues(ctx, node.SpaceID, parseStringSliceJSON(node.SupportedWorkloads)); err != nil {
		return "", err
	}
	if err := s.updateSCFFunctionCode(ctx, *node, *pkg, item.GetEnvironment(), item.GetConfig(), watchdogEnabled); err != nil {
		return "", err
	}
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpdateNodeDeployment(persistCtx, spaceID, item.GetNodeId(), item.GetPackageId(), pkg.Version); err != nil {
		return "", err
	}
	return fmt.Sprintf("deployed package %s to %s", pkg.PackageID, node.NodeID), nil
}

func (s *Service) ReportHeartbeat(ctx context.Context, req *pb.ReportHeartbeatReq) (*pb.ReportHeartbeatRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	spaceID := req.GetSpaceId()
	log.InfoContextf(ctx, "[CloudNode] heartbeat space=%s node_id=%s node_type=%s source=%s version=%s",
		spaceID, req.GetNodeId(), req.GetNodeType(), req.GetSourceService(), req.GetRunningVersion())
	if s.heartbeatSink != nil {
		if err := s.heartbeatSink.Enqueue(req); err != nil {
			log.WarnContextf(ctx, "[CloudNode] heartbeat enqueue failed: %v", err)
			return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		return &pb.ReportHeartbeatRsp{RetInfo: retOK()}, nil
	}
	if req.GetNodeId() != "" {
		supported, _ := json.Marshal(req.GetSupportedWorkloads())
		metadata, _ := json.Marshal(req.GetMetadata().AsMap())
		if err := s.catalog.UpdateHeartbeat(ctx, spaceID, req.GetNodeId(), req.GetRunningVersion(), string(supported), string(metadata)); err != nil {
			log.WarnContextf(ctx, "[CloudNode] heartbeat upsert node failed: %v", err)
		}
	}
	return &pb.ReportHeartbeatRsp{RetInfo: retOK()}, nil
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
	node.PackageVersion = pkg.Version
	metadata := parseJSONMap(node.Metadata)
	config := item.GetConfig()
	if clsTopicID := strings.TrimSpace(config["cls_topic_id"]); clsTopicID != "" {
		metadata["cls_topic_id"] = clsTopicID
	}
	if clsLogsetID := strings.TrimSpace(config["cls_logset_id"]); clsLogsetID != "" {
		metadata["cls_logset_id"] = clsLogsetID
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
		if remoteSCFWatchdogEnabled(info.Environment) != metadataBool(parseJSONMap(node.Metadata), scfWatchdogMetadataKey) {
			return fmt.Errorf("scf function %s watchdog selection does not match catalog", ref.FunctionName)
		}
		info, err = waitForSCFActive(ctx, client, ref, info)
		if err != nil {
			return err
		}
		mergeSCFFunctionMetadata(node, info)
		// A previous attempt may have created the function before CloudNode was
		// interrupted. The package marker proves this is the accepted create,
		// rather than an unrelated function with the same stable name.
		return nil
	}
	if !isSCFNotFound(err) {
		return fmt.Errorf("get scf function %s: %w", ref.FunctionName, err)
	}
	watchdogEnabled := metadataBool(parseJSONMap(node.Metadata), scfWatchdogMetadataKey)
	environment := scfWatchdogEnvironment(item.GetEnvironment(), watchdogEnabled)
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	createCtx, createCancel := context.WithTimeout(ctx, scfCreateAttemptTimeout)
	_, err = client.CreateFunction(createCtx, tencentscf.CreateFunctionRequest{
		FunctionRef: ref,
		Runtime:     firstString(item.GetRuntime(), pkg.Runtime, "CustomRuntime"),
		Handler:     firstString(item.GetHandler(), "main"),
		Description: fmt.Sprintf("MooX cloud function node %s", node.NodeID),
		MemorySize:  configInt64(config, "memory_size", 256),
		Timeout:     configInt64(config, "timeout", defaultSCFTimeoutSeconds),
		Environment: environment,
		COSBucket:   pkg.COSBucket,
		COSRegion:   firstString(pkg.COSRegion, account.COSRegion),
		COSObject:   strings.TrimPrefix(pkg.COSPath, "/"),
		ClsLogsetID: strings.TrimSpace(config["cls_logset_id"]),
		ClsTopicID:  strings.TrimSpace(config["cls_topic_id"]),
		Type:        firstString(config["function_type"], "Event"),
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
		if remotePackageID != pkg.PackageID {
			return fmt.Errorf(
				"create scf function %s returned an ambiguous error and remote code package is %q, expected %q: %w",
				ref.FunctionName,
				remotePackageID,
				pkg.PackageID,
				createErr,
			)
		}
		info, err = waitForSCFActive(reconcileCtx, client, ref, info)
		if err != nil {
			return err
		}
		mergeSCFFunctionMetadata(node, info)
		return nil
	}
	info, err = waitForSCFActive(ctx, client, ref, nil)
	if err != nil {
		return err
	}
	mergeSCFFunctionMetadata(node, info)
	return nil
}

// ensureLocalPackage makes the local E2E/runtime profile independent of Tencent
// credentials and COS. Local nodes execute through the durable JobItem queue,
// so they only need an available package descriptor, not a remote SCF function.
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

func mergeSCFFunctionMetadata(node *store.CloudNode, info *tencentscf.FunctionInfo) {
	if node == nil || info == nil {
		return
	}
	metadata := parseJSONMap(node.Metadata)
	changed := false
	if clsLogsetID := strings.TrimSpace(info.ClsLogsetID); clsLogsetID != "" {
		metadata["cls_logset_id"] = clsLogsetID
		changed = true
	}
	if clsTopicID := strings.TrimSpace(info.ClsTopicID); clsTopicID != "" {
		metadata["cls_topic_id"] = clsTopicID
		changed = true
	}
	if changed {
		node.Metadata = jsonString(metadata)
	}
}

func (s *Service) updateSCFFunctionCode(
	ctx context.Context,
	node store.CloudNode,
	pkg store.FunctionPackage,
	desiredEnvironment map[string]string,
	desiredConfig map[string]string,
	watchdogEnabled bool,
) error {
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
	info, err := client.GetFunction(ctx, ref)
	if err != nil {
		return fmt.Errorf("get scf function %s before deploy: %w", ref.FunctionName, err)
	}
	// MOOX_CODE_PACKAGE_ID is written only after the code update has completed
	// and the desired configuration update has been accepted. It therefore
	// doubles as the idempotency marker when the caller times out while Tencent
	// is still returning Updating.
	codeCurrent := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"]) == pkg.PackageID
	watchdogCurrent := remoteSCFWatchdogEnabled(info.Environment) == watchdogEnabled
	if codeCurrent && watchdogCurrent && len(desiredEnvironment) == 0 && len(desiredConfig) == 0 {
		_, err = waitForSCFActive(ctx, client, ref, info)
		return err
	}
	info, err = waitForSCFActive(ctx, client, ref, info)
	if err != nil {
		return err
	}
	if !codeCurrent {
		_, err = client.UpdateFunctionCode(ctx, tencentscf.UpdateFunctionCodeRequest{
			FunctionRef: ref,
			Handler:     firstString(metadataString(metadata, "handler"), "main"),
			COSBucket:   pkg.COSBucket,
			COSRegion:   firstString(pkg.COSRegion, account.COSRegion),
			COSObject:   strings.TrimPrefix(pkg.COSPath, "/"),
		})
		if err != nil {
			return fmt.Errorf("update scf function %s: %w", firstString(node.FunctionName, node.NodeID), err)
		}
		if _, err := waitForSCFActive(ctx, client, ref, nil); err != nil {
			return err
		}
	}
	environment := copyStringMap(desiredEnvironment)
	if len(environment) == 0 {
		environment = copyStringMap(info.Environment)
		if environment == nil {
			environment = make(map[string]string)
		}
	}
	environment = scfWatchdogEnvironment(environment, watchdogEnabled)
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{
		FunctionRef: ref,
		Environment: environment,
		MemorySize:  configInt64(desiredConfig, "memory_size", 0),
		Timeout:     configInt64(desiredConfig, "timeout", 0),
		ClsLogsetID: strings.TrimSpace(desiredConfig["cls_logset_id"]),
		ClsTopicID:  strings.TrimSpace(desiredConfig["cls_topic_id"]),
	}); err != nil {
		return fmt.Errorf("update scf function %s configuration: %w", ref.FunctionName, err)
	}
	_, err = waitForSCFActive(ctx, client, ref, nil)
	return err
}

func (s *Service) ensureSingleSCFWatchdog(ctx context.Context, spaceID, nodeID string) error {
	nodes, err := s.catalog.ListSCFEventNodes(ctx)
	if err != nil {
		return fmt.Errorf("list SCF nodes before watchdog selection: %w", err)
	}
	for _, node := range nodes {
		if node.SpaceID != spaceID || node.NodeID == nodeID {
			continue
		}
		if metadataBool(parseJSONMap(node.Metadata), scfWatchdogMetadataKey) {
			return fmt.Errorf(
				"space %s already has SCF watchdog sentinel node %s",
				spaceID,
				node.NodeID,
			)
		}
	}
	return nil
}

func requestedSCFWatchdog(environment map[string]string) (enabled bool, specified bool, err error) {
	raw, ok := environment[scfWatchdogEnvironmentKey]
	if !ok {
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("%s must be true or false", scfWatchdogEnvironmentKey)
	}
}

func remoteSCFWatchdogEnabled(environment map[string]string) bool {
	enabled, _, err := requestedSCFWatchdog(environment)
	return err == nil && enabled
}

func scfWatchdogEnvironment(environment map[string]string, enabled bool) map[string]string {
	result := copyStringMap(environment)
	if result == nil {
		result = make(map[string]string)
	}
	if enabled {
		result[scfWatchdogEnvironmentKey] = "true"
	} else {
		delete(result, scfWatchdogEnvironmentKey)
	}
	return result
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
			"%s-%s-%s-%s",
			prefix,
			sanitizeSCFFunctionToken(spaceID),
			firstString(item.GetRegion(), "region"),
			indexSuffix,
		),
	)
	nodeID := firstString(metadataString(metadata, "node_id"), functionName)
	return store.CloudNode{
		SpaceID:            spaceID,
		NodeID:             nodeID,
		CloudAccountID:     item.GetCloudAccountId(),
		PackageID:          item.GetPackageId(),
		DeploymentID:       firstString(item.GetDeploymentId(), metadataString(metadata, "deployment_id")),
		NodeType:           firstString(item.GetNodeType(), "scf-event"),
		Provider:           "tencent-scf",
		Region:             item.GetRegion(),
		Namespace:          firstString(item.GetNamespace(), "default"),
		FunctionName:       functionName,
		SupportedWorkloads: supportedWorkloadsFromMetadata(metadata),
		Metadata:           jsonString(metadata),
		Status:             "unknown",
		IsDeleted:          false,
	}
}

func sanitizeSCFFunctionToken(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		raw = "space"
	}
	var b strings.Builder
	for _, r := range raw {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	token := strings.Trim(b.String(), "-_")
	if token == "" {
		token = "space"
	}
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%s-%x", token, sum[:4])
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
	if node.GetRunningVersion() != "" {
		next.RunningVersion = node.GetRunningVersion()
	}
	if len(node.GetSupportedWorkloads()) > 0 {
		raw, _ := json.Marshal(node.GetSupportedWorkloads())
		next.SupportedWorkloads = string(raw)
	}
	metadata := nodeMetadataFromPB(node)
	if len(metadata) > 0 {
		next.Metadata = mergeMetadataJSON(existing.Metadata, jsonString(metadata))
	}
	if node.GetStatus() != pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED {
		next.Status = nodeStatusToDB(node.GetStatus())
	}
	next.IsDeleted = node.GetIsDeleted()
	return next
}

func toPBNode(node store.CloudNode) *pb.CloudNode {
	lastHeartbeat := ""
	if node.LastHeartbeatAt != nil {
		lastHeartbeat = node.LastHeartbeatAt.UTC().Format(time.RFC3339)
	}
	metadata := parseJSONMap(node.Metadata)
	st, _ := structpb.NewStruct(metadata)
	if st == nil {
		st = &structpb.Struct{}
	}
	return &pb.CloudNode{
		Id:                 int32(node.ID),
		SpaceId:            node.SpaceID,
		NodeId:             node.NodeID,
		CloudAccountId:     node.CloudAccountID,
		PackageId:          node.PackageID,
		PackageVersion:     node.PackageVersion,
		DeploymentId:       node.DeploymentID,
		RunningVersion:     node.RunningVersion,
		Namespace:          node.Namespace,
		NodeType:           node.NodeType,
		Provider:           node.Provider,
		FunctionName:       node.FunctionName,
		BizType:            metadataString(metadata, "biz_type"),
		Region:             node.Region,
		Tag:                metadataString(metadata, "tag"),
		IpAddress:          metadataString(metadata, "ip_address"),
		SupportedWorkloads: parseStringSliceJSON(node.SupportedWorkloads),
		Metadata:           st,
		TimeoutThreshold:   metadataInt32(metadata, "timeout_threshold"),
		HeartbeatInterval:  metadataInt32(metadata, "heartbeat_interval"),
		ProbeEnabled:       metadataBool(metadata, "probe_enabled"),
		ProbeUrl:           metadataString(metadata, "probe_url"),
		Status:             nodeStatusToPB(node.Status),
		LastHeartbeat:      lastHeartbeat,
		IsDeleted:          node.IsDeleted,
		CreateTime:         formatTime(node.CreateTime),
		ModifyTime:         formatTime(node.ModifyTime),
		ClsTopicId:         metadataString(metadata, "cls_topic_id"),
	}
}

func fromPBNode(spaceID string, node *pb.CloudNode) store.CloudNode {
	metadata := nodeMetadataFromPB(node)
	supported := "[]"
	if len(node.GetSupportedWorkloads()) > 0 {
		raw, _ := json.Marshal(node.GetSupportedWorkloads())
		supported = string(raw)
	} else if workloads := supportedWorkloadsFromMetadata(metadata); workloads != "[]" {
		supported = workloads
	}
	return store.CloudNode{
		SpaceID:            spaceID,
		NodeID:             node.GetNodeId(),
		CloudAccountID:     node.GetCloudAccountId(),
		PackageID:          node.GetPackageId(),
		PackageVersion:     node.GetPackageVersion(),
		DeploymentID:       node.GetDeploymentId(),
		NodeType:           firstString(node.GetNodeType(), "scf-event"),
		Provider:           firstString(node.GetProvider(), "tencent-scf"),
		Region:             node.GetRegion(),
		Namespace:          node.GetNamespace(),
		FunctionName:       firstString(node.GetFunctionName(), metadataString(metadata, "function_name"), node.GetNodeId()),
		RunningVersion:     node.GetRunningVersion(),
		SupportedWorkloads: supported,
		Metadata:           jsonString(metadata),
		Status:             nodeStatusToDB(node.GetStatus()),
		IsDeleted:          node.GetIsDeleted(),
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
	if node.GetTimeoutThreshold() != 0 {
		metadata["timeout_threshold"] = node.GetTimeoutThreshold()
	}
	if node.GetHeartbeatInterval() != 0 {
		metadata["heartbeat_interval"] = node.GetHeartbeatInterval()
	}
	if node.GetProbeEnabled() {
		metadata["probe_enabled"] = true
	}
	if node.GetProbeUrl() != "" {
		metadata["probe_url"] = node.GetProbeUrl()
	}
	if node.GetClsTopicId() != "" {
		metadata["cls_topic_id"] = node.GetClsTopicId()
	}
	return metadata
}

func nodeStatusToPB(status string) pb.NodeStatusCode {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online":
		return pb.NodeStatusCode_NODE_STATUS_ONLINE
	case "timeout":
		return pb.NodeStatusCode_NODE_STATUS_TIMEOUT
	case "abnormal":
		return pb.NodeStatusCode_NODE_STATUS_ABNORMAL
	case "offline", "deleted", "unknown":
		return pb.NodeStatusCode_NODE_STATUS_OFFLINE
	default:
		return pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED
	}
}

func nodeStatusToDB(status pb.NodeStatusCode) string {
	switch status {
	case pb.NodeStatusCode_NODE_STATUS_ONLINE:
		return "online"
	case pb.NodeStatusCode_NODE_STATUS_TIMEOUT:
		return "timeout"
	case pb.NodeStatusCode_NODE_STATUS_ABNORMAL:
		return "abnormal"
	case pb.NodeStatusCode_NODE_STATUS_OFFLINE:
		return "offline"
	default:
		return "unknown"
	}
}

func parseStringSliceJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
