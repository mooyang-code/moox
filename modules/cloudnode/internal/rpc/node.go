package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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
		pkgVersion := ""
		if pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId()); err == nil && pkg != nil {
			pkgVersion = pkg.Version
		}
		if err := s.catalog.UpdateNodeDeployment(ctx, spaceID, item.GetNodeId(), item.GetPackageId(), pkgVersion); err != nil {
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
	if req.GetNodeId() != "" {
		supported, _ := json.Marshal(req.GetSupportedWorkloads())
		metadata, _ := json.Marshal(req.GetMetadata().AsMap())
		if err := s.catalog.UpsertHeartbeat(ctx, spaceID, req.GetNodeId(), firstString(req.GetNodeType(), "scf-event"), req.GetRunningVersion(), string(supported), string(metadata)); err != nil {
			log.WarnContextf(ctx, "[CloudNode] heartbeat upsert node failed: %v", err)
		}
	}
	return &pb.ReportHeartbeatRsp{RetInfo: retOK()}, nil
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
		Id:                  int32(node.ID),
		SpaceId:             node.SpaceID,
		NodeId:              node.NodeID,
		CloudAccountId:      node.CloudAccountID,
		PackageId:           node.PackageID,
		PackageVersion:      node.PackageVersion,
		DeploymentId:        node.DeploymentID,
		RunningVersion:      node.RunningVersion,
		Namespace:           node.Namespace,
		NodeType:            node.NodeType,
		Provider:            node.Provider,
		FunctionName:        node.FunctionName,
		BizType:             metadataString(metadata, "biz_type"),
		Region:              node.Region,
		Tag:                 metadataString(metadata, "tag"),
		IpAddress:           metadataString(metadata, "ip_address"),
		SupportedWorkloads:  parseStringSliceJSON(node.SupportedWorkloads),
		Metadata:            st,
		TimeoutThreshold:    metadataInt32(metadata, "timeout_threshold"),
		HeartbeatInterval:   metadataInt32(metadata, "heartbeat_interval"),
		ProbeEnabled:        metadataBool(metadata, "probe_enabled"),
		ProbeUrl:            metadataString(metadata, "probe_url"),
		Status:              nodeStatusToPB(node.Status),
		LastHeartbeat:       lastHeartbeat,
		IsDeleted:           node.IsDeleted,
		CreateTime:          formatTime(node.CreateTime),
		ModifyTime:          formatTime(node.ModifyTime),
		ClsTopicId:          metadataString(metadata, "cls_topic_id"),
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
