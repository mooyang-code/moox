package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
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
	if err := s.catalog.UpsertNode(ctx, node); err != nil {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.UpdateNodeRsp{RetInfo: retOK()}, nil
}

func (s *Service) BatchCreateNodes(ctx context.Context, req *pb.BatchCreateNodesReq) (*pb.BatchChangeResult, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	nodes := req.GetNodes()
	if len(nodes) == 0 {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes is required")}, nil
	}
	created := 0
	for i, item := range nodes {
		if item == nil {
			continue
		}
		node := cloudNodeFromCreateItem(spaceID, item, i)
		if node.CloudAccountID == "" {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes.cloud_account_id is required")}, nil
		}
		if node.Region == "" {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes.region is required")}, nil
		}
		if err := s.ensureSCFFunction(ctx, &node, item); err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		if err := s.catalog.UpsertNode(ctx, node); err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		created++
	}
	return &pb.BatchChangeResult{
		RetInfo:        retOK(),
		BatchId:        directBatchID("create_nodes"),
		ProcessedCount: int32(created),
	}, nil
}

func (s *Service) BatchDeleteNodes(ctx context.Context, req *pb.BatchDeleteNodesReq) (*pb.BatchChangeResult, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	nodeIDs := compactStrings(req.GetNodeIds())
	if len(nodeIDs) == 0 {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_ids is required")}, nil
	}
	if err := s.catalog.DeleteNodes(ctx, spaceID, nodeIDs); err != nil {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.BatchChangeResult{
		RetInfo:        retOK(),
		BatchId:        directBatchID("delete_nodes"),
		ProcessedCount: int32(len(nodeIDs)),
	}, nil
}

func (s *Service) BatchDeployNodes(ctx context.Context, req *pb.BatchDeployNodesReq) (*pb.BatchChangeResult, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	deployments := req.GetDeployments()
	if len(deployments) == 0 {
		return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "deployments is required")}, nil
	}
	updated := 0
	for _, item := range deployments {
		if item == nil {
			continue
		}
		if item.GetNodeId() == "" || item.GetPackageId() == "" {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "deployments.node_id and package_id are required")}, nil
		}
		node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
		if err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		if node == nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "node not found")}, nil
		}
		pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId())
		if err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		if pkg == nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "package not found")}, nil
		}
		if err := s.updateSCFFunctionCode(ctx, *node, *pkg); err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		if err := s.catalog.UpdateNodeDeployment(ctx, spaceID, item.GetNodeId(), item.GetPackageId(), pkg.Version); err != nil {
			return &pb.BatchChangeResult{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		updated++
	}
	return &pb.BatchChangeResult{
		RetInfo:        retOK(),
		BatchId:        directBatchID("deploy_nodes"),
		ProcessedCount: int32(updated),
	}, nil
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
	directives, err := s.heartbeatDirectives(ctx, spaceID, req.GetNodeId())
	if err != nil {
		log.WarnContextf(ctx, "[CloudNode] heartbeat directives query failed: %v", err)
	}
	if s.heartbeatSink != nil {
		if err := s.heartbeatSink.Enqueue(req); err != nil {
			log.WarnContextf(ctx, "[CloudNode] heartbeat enqueue failed: %v", err)
			return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		return &pb.ReportHeartbeatRsp{RetInfo: retOK(), Directives: directives}, nil
	}
	if req.GetNodeId() != "" {
		supported, _ := json.Marshal(req.GetSupportedWorkloads())
		metadata, _ := json.Marshal(req.GetMetadata().AsMap())
		if err := s.catalog.UpsertHeartbeat(ctx, spaceID, req.GetNodeId(), firstString(req.GetNodeType(), "scf-event"), req.GetRunningVersion(), string(supported), string(metadata)); err != nil {
			log.WarnContextf(ctx, "[CloudNode] heartbeat upsert node failed: %v", err)
		}
	}
	return &pb.ReportHeartbeatRsp{RetInfo: retOK(), Directives: directives}, nil
}

func (s *Service) heartbeatDirectives(ctx context.Context, spaceID string, nodeID string) ([]*pb.ControlDirective, error) {
	if s.jobState != nil {
		return s.jobState.ListCancelDirectives(ctx, spaceID, nodeID, 20)
	}
	return nil, nil
}

func (s *Service) ensureSCFFunction(ctx context.Context, node *repository.CloudNode, item *pb.NodeCreateItem) error {
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

	client := s.scfClient(*account)
	ref := tencentscf.FunctionRef{
		Region:       node.Region,
		FunctionName: firstString(node.FunctionName, node.NodeID),
		Namespace:    firstString(node.Namespace, "default"),
	}
	info, err := client.GetFunction(ctx, ref)
	if err == nil {
		mergeSCFFunctionMetadata(node, info)
		return s.updateSCFFunctionCode(ctx, *node, *pkg)
	}
	if !isSCFNotFound(err) {
		return fmt.Errorf("get scf function %s: %w", ref.FunctionName, err)
	}
	_, err = client.CreateFunction(ctx, tencentscf.CreateFunctionRequest{
		FunctionRef: ref,
		Runtime:     firstString(item.GetRuntime(), pkg.Runtime, "CustomRuntime"),
		Handler:     firstString(item.GetHandler(), "main"),
		Description: fmt.Sprintf("MooX cloud function node %s", node.NodeID),
		MemorySize:  configInt64(config, "memory_size", 256),
		Timeout:     configInt64(config, "timeout", 60),
		Environment: copyStringMap(item.GetEnvironment()),
		COSBucket:   pkg.COSBucket,
		COSRegion:   firstString(pkg.COSRegion, account.COSRegion),
		COSObject:   strings.TrimPrefix(pkg.COSPath, "/"),
		ClsLogsetID: strings.TrimSpace(config["cls_logset_id"]),
		ClsTopicID:  strings.TrimSpace(config["cls_topic_id"]),
		Type:        firstString(config["function_type"], "Event"),
	})
	if err != nil {
		return fmt.Errorf("create scf function %s: %w", ref.FunctionName, err)
	}
	info, err = client.GetFunction(ctx, ref)
	if err != nil {
		log.WarnContextf(ctx, "[CloudNode] get scf function metadata after create failed, function=%s namespace=%s region=%s err=%v",
			ref.FunctionName, ref.Namespace, ref.Region, err)
		return nil
	}
	mergeSCFFunctionMetadata(node, info)
	return nil
}

func mergeSCFFunctionMetadata(node *repository.CloudNode, info *tencentscf.FunctionInfo) {
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

func (s *Service) updateSCFFunctionCode(ctx context.Context, node repository.CloudNode, pkg repository.FunctionPackage) error {
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
	_, err = s.scfClient(*account).UpdateFunctionCode(ctx, tencentscf.UpdateFunctionCodeRequest{
		FunctionRef: tencentscf.FunctionRef{
			Region:       node.Region,
			FunctionName: firstString(node.FunctionName, node.NodeID),
			Namespace:    firstString(node.Namespace, "default"),
		},
		Handler:   firstString(metadataString(metadata, "handler"), "main"),
		COSBucket: pkg.COSBucket,
		COSRegion: firstString(pkg.COSRegion, account.COSRegion),
		COSObject: strings.TrimPrefix(pkg.COSPath, "/"),
	})
	if err != nil {
		return fmt.Errorf("update scf function %s: %w", firstString(node.FunctionName, node.NodeID), err)
	}
	return nil
}

func (s *Service) packageAndAccount(ctx context.Context, spaceID string, packageID string, accountID string) (*repository.FunctionPackage, *repository.CloudAccount, error) {
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

func (s *Service) scfClient(account repository.CloudAccount) scfProvisioner {
	factory := s.scfClientFactory
	if factory == nil {
		factory = defaultSCFClientFactory
	}
	return factory(account)
}

func isTencentProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "tencent", "tencent-scf":
		return true
	default:
		return false
	}
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

func cloudNodeFromCreateItem(spaceID string, item *pb.NodeCreateItem, index int) repository.CloudNode {
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
	if len(item.GetEnvironment()) > 0 {
		metadata["environment"] = item.GetEnvironment()
	}
	prefix := firstString(metadataString(metadata, "function_name_prefix"), "moox-cloudnode")
	indexSuffix := firstString(metadataString(metadata, "index"), strconv.Itoa(index))
	functionName := firstString(
		metadataString(metadata, "function_name"),
		fmt.Sprintf("%s-%s-%s", prefix, firstString(item.GetRegion(), "region"), indexSuffix),
	)
	nodeID := firstString(metadataString(metadata, "node_id"), functionName)
	return repository.CloudNode{
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

func mergeNodeUpdate(existing repository.CloudNode, node *pb.CloudNode) repository.CloudNode {
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

func toPBNode(node repository.CloudNode) *pb.CloudNode {
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

func fromPBNode(spaceID string, node *pb.CloudNode) repository.CloudNode {
	metadata := nodeMetadataFromPB(node)
	supported := "[]"
	if len(node.GetSupportedWorkloads()) > 0 {
		raw, _ := json.Marshal(node.GetSupportedWorkloads())
		supported = string(raw)
	} else if workloads := supportedWorkloadsFromMetadata(metadata); workloads != "[]" {
		supported = workloads
	}
	return repository.CloudNode{
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
