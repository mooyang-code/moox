package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	nodeBatchOperationCreate = "create_nodes"
	nodeBatchOperationDeploy = "deploy_nodes"
	maxNodeBatchItems        = 100
)

func (s *Service) SubmitCreateNodes(ctx context.Context, req *pb.BatchCreateNodesReq) (*pb.SubmitNodeBatchRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	items := req.GetNodes()
	if ret := validateNodeBatchSize(len(items), "nodes"); ret != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
	}

	jobID := "node-batch-" + uuid.NewString()
	creates := make([]store.NodeBatchItemCreate, 0, len(items))
	seenNodes := make(map[string]struct{}, len(items))
	for index, item := range items {
		node, ret := s.preflightCreateNode(ctx, spaceID, item, index)
		if ret != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
		}
		if _, exists := seenNodes[node.NodeID]; exists {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes contains duplicate node_id")}, nil
		}
		seenNodes[node.NodeID] = struct{}{}
		raw, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid node request")}, nil
		}
		creates = append(creates, store.NodeBatchItemCreate{
			ItemID:      fmt.Sprintf("%s-%03d", jobID, index),
			ItemIndex:   index,
			NodeID:      node.NodeID,
			RequestJSON: string(raw),
		})
	}
	if err := s.catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{
		SpaceID: spaceID, JobID: jobID, Operation: nodeBatchOperationCreate, Items: creates,
	}); err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitNodeBatchRsp{
		RetInfo: retOK(), JobId: jobID,
		Operation:  pb.NodeBatchOperation_NODE_BATCH_OPERATION_CREATE_NODES,
		TotalCount: int32(len(creates)),
	}, nil
}

func (s *Service) SubmitDeployNodes(ctx context.Context, req *pb.BatchDeployNodesReq) (*pb.SubmitNodeBatchRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	items := req.GetDeployments()
	if ret := validateNodeBatchSize(len(items), "deployments"); ret != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
	}

	jobID := "node-batch-" + uuid.NewString()
	creates := make([]store.NodeBatchItemCreate, 0, len(items))
	seenNodes := make(map[string]struct{}, len(items))
	for index, item := range items {
		if ret := s.preflightDeployNode(ctx, spaceID, item); ret != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
		}
		nodeID := strings.TrimSpace(item.GetNodeId())
		if _, exists := seenNodes[nodeID]; exists {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "deployments contains duplicate node_id")}, nil
		}
		seenNodes[nodeID] = struct{}{}
		raw, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid deployment request")}, nil
		}
		creates = append(creates, store.NodeBatchItemCreate{
			ItemID:      fmt.Sprintf("%s-%03d", jobID, index),
			ItemIndex:   index,
			NodeID:      nodeID,
			RequestJSON: string(raw),
		})
	}
	if err := s.catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{
		SpaceID: spaceID, JobID: jobID, Operation: nodeBatchOperationDeploy, Items: creates,
	}); err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitNodeBatchRsp{
		RetInfo: retOK(), JobId: jobID,
		Operation:  pb.NodeBatchOperation_NODE_BATCH_OPERATION_DEPLOY_NODES,
		TotalCount: int32(len(creates)),
	}, nil
}

func (s *Service) GetNodeBatchChange(ctx context.Context, req *pb.GetNodeBatchChangeReq) (*pb.GetNodeBatchChangeRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetNodeBatchChangeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	jobID := strings.TrimSpace(req.GetJobId())
	if jobID == "" {
		return &pb.GetNodeBatchChangeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id is required")}, nil
	}
	aggregate, err := s.catalog.GetNodeBatch(ctx, spaceID, jobID)
	if err != nil {
		return &pb.GetNodeBatchChangeRsp{RetInfo: retFromError(err)}, nil
	}
	if aggregate == nil {
		return &pb.GetNodeBatchChangeRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "node batch not found")}, nil
	}
	completed := aggregate.SuccessCount + aggregate.FailedCount
	progress := 0
	if aggregate.Job.TotalCount > 0 {
		progress = completed * 100 / aggregate.Job.TotalCount
	}
	rsp := &pb.GetNodeBatchChangeRsp{
		RetInfo: retOK(),
		Job: &pb.NodeBatchSummary{
			JobId: jobID, Operation: nodeBatchOperationToPB(aggregate.Job.Operation),
			Status:     nodeBatchStatusToPB(aggregate.Job.Status),
			TotalCount: int32(aggregate.Job.TotalCount), PendingCount: int32(aggregate.PendingCount),
			RunningCount: int32(aggregate.RunningCount), SuccessCount: int32(aggregate.SuccessCount),
			FailedCount: int32(aggregate.FailedCount), ProgressPercent: int32(progress),
			CreatedAt: formatTime(aggregate.Job.CreateTime),
		},
		Items: make([]*pb.NodeBatchItemResult, 0, len(aggregate.Items)),
	}
	if aggregate.Job.CompletedAt != nil {
		rsp.Job.CompletedAt = formatTime(*aggregate.Job.CompletedAt)
	}
	for _, item := range aggregate.Items {
		out := &pb.NodeBatchItemResult{
			ItemId: item.ItemID, NodeId: item.NodeID,
			Status:        nodeBatchItemStatusToPB(item.Status),
			ResultSummary: item.ResultSummary, ErrorMessage: item.ErrorMessage,
		}
		if item.StartedAt != nil {
			out.StartedAt = formatTime(*item.StartedAt)
		}
		if item.CompletedAt != nil {
			out.CompletedAt = formatTime(*item.CompletedAt)
		}
		rsp.Items = append(rsp.Items, out)
	}
	return rsp, nil
}

func validateNodeBatchSize(count int, field string) *pb.RetInfo {
	if count < 1 || count > maxNodeBatchItems {
		return retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("%s must contain 1..%d items", field, maxNodeBatchItems))
	}
	return nil
}

func (s *Service) preflightCreateNode(
	ctx context.Context,
	spaceID string,
	item *pb.NodeCreateItem,
	index int,
) (store.CloudNode, *pb.RetInfo) {
	if item == nil {
		return store.CloudNode{}, retErr(pb.ErrorCode_INVALID_PARAM, "nodes item is required")
	}
	node := cloudNodeFromCreateItem(spaceID, item, index)
	if strings.TrimSpace(node.CloudAccountID) == "" || strings.TrimSpace(node.Region) == "" || strings.TrimSpace(node.PackageID) == "" {
		return store.CloudNode{}, retErr(pb.ErrorCode_INVALID_PARAM, "nodes.cloud_account_id, region and package_id are required")
	}
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil {
		return store.CloudNode{}, retFromError(err)
	}
	if account == nil {
		return store.CloudNode{}, retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, node.PackageID)
	if err != nil {
		return store.CloudNode{}, retFromError(err)
	}
	if pkg == nil {
		return store.CloudNode{}, retErr(pb.ErrorCode_NOT_FOUND, "package not found")
	}
	if pkg.Status != "available" {
		return store.CloudNode{}, retErr(pb.ErrorCode_INVALID_PARAM, "package is not available")
	}
	return node, nil
}

func (s *Service) preflightDeployNode(ctx context.Context, spaceID string, item *pb.NodeDeployItem) *pb.RetInfo {
	if item == nil || strings.TrimSpace(item.GetNodeId()) == "" || strings.TrimSpace(item.GetPackageId()) == "" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "deployments.node_id and package_id are required")
	}
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil {
		return retFromError(err)
	}
	if node == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "node not found")
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId())
	if err != nil {
		return retFromError(err)
	}
	if pkg == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "package not found")
	}
	if pkg.Status != "available" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "package is not available")
	}
	return nil
}

func nodeBatchOperationToPB(operation string) pb.NodeBatchOperation {
	switch operation {
	case nodeBatchOperationCreate:
		return pb.NodeBatchOperation_NODE_BATCH_OPERATION_CREATE_NODES
	case nodeBatchOperationDeploy:
		return pb.NodeBatchOperation_NODE_BATCH_OPERATION_DEPLOY_NODES
	default:
		return pb.NodeBatchOperation_NODE_BATCH_OPERATION_UNSPECIFIED
	}
}

func nodeBatchStatusToPB(status string) pb.NodeBatchStatus {
	switch status {
	case store.NodeBatchPending:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING
	case store.NodeBatchRunning:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING
	case store.NodeBatchSuccess:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS
	case store.NodeBatchFailed:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED
	case store.NodeBatchPartial:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL
	default:
		return pb.NodeBatchStatus_NODE_BATCH_STATUS_UNSPECIFIED
	}
}

func nodeBatchItemStatusToPB(status string) pb.NodeBatchItemStatus {
	switch status {
	case store.NodeBatchPending:
		return pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_PENDING
	case store.NodeBatchRunning:
		return pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_RUNNING
	case store.NodeBatchSuccess:
		return pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_SUCCESS
	case store.NodeBatchFailed:
		return pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_FAILED
	default:
		return pb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_UNSPECIFIED
	}
}
