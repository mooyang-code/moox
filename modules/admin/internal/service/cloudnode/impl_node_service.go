package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 节点查询 ==========

// GetNodeList 获取云节点列表（支持分页）
func (s *ServiceImpl) GetNodeList(ctx context.Context, req *pb.GetNodeListReq) (*pb.GetNodeListRsp, error) {
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}
	q := req.GetQuery()
	if q == nil {
		q = &pb.NodeListRequest{}
	}

	page := int(q.GetPage())
	if page <= 0 {
		page = 1
	}
	pageSize := int(q.GetPageSize())
	if pageSize <= 0 {
		pageSize = 20
	}

	// 构建DAO查询参数
	query := &dao.NodeListQuery{
		Page:           page,
		PageSize:       pageSize,
		NodeID:         q.GetNodeId(),
		CloudAccountID: q.GetCloudAccountId(),
		Namespace:      q.GetNamespace(),
		Region:         q.GetRegion(),
		NodeType:       q.GetNodeType(),
		BizType:        q.GetBizType(),
		Tag:            q.GetTag(),
		Status:         q.GetStatus(),
		Keyword:        q.GetKeyword(),
	}

	// 如果有状态过滤，需要获取在线节点ID列表传给DAO
	if q.GetStatus() != "" {
		query.OnlineNodeIDs = s.heartbeatStore.GetOnlineNodeIDs()
	}

	// 查询数据
	nodes, total, err := s.nodeDAO.GetCloudNodeList(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get node list: %w", err)
	}

	// 转换为PB并合并心跳数据
	items := make([]*pb.CloudNode, len(nodes))
	for i, node := range nodes {
		items[i] = s.convertNodeWithHeartbeat(node)
	}

	return &pb.GetNodeListRsp{
		Items: items,
		Total: total,
		Page:  int32(page),
		Size:  int32(len(items)),
	}, nil
}

// GetCloudNode 根据节点ID获取云节点详情
func (s *ServiceImpl) GetCloudNode(ctx context.Context, nodeID string) (*pb.CloudNode, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return nil, fmt.Errorf("node not found")
	}
	return s.convertNodeWithHeartbeat(node), nil
}

// GetNodesByType 根据节点类型获取云节点列表
func (s *ServiceImpl) GetNodesByType(ctx context.Context, nodeType string) ([]*pb.CloudNode, error) {
	nodes, err := s.nodeDAO.GetCloudNodesByType(ctx, nodeType)
	if err != nil {
		return nil, err
	}
	result := make([]*pb.CloudNode, len(nodes))
	for i, node := range nodes {
		result[i] = s.convertNodeWithHeartbeat(node)
	}
	return result, nil
}

// GetOnlineNodes 获取所有在线云节点列表
func (s *ServiceImpl) GetOnlineNodes(ctx context.Context) ([]*pb.CloudNode, error) {
	onlineNodeIDs := s.heartbeatStore.GetOnlineNodeIDs()
	if len(onlineNodeIDs) == 0 {
		return []*pb.CloudNode{}, nil
	}

	nodes, err := s.nodeDAO.GetNodesByIDs(ctx, onlineNodeIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get online nodes: %w", err)
	}

	result := make([]*pb.CloudNode, len(nodes))
	for i, node := range nodes {
		result[i] = s.convertNodeWithHeartbeat(node)
	}
	return result, nil
}

// convertNodeWithHeartbeat 将 model.CloudNode 转为 pb.CloudNode，并合并内存心跳数据。
func (s *ServiceImpl) convertNodeWithHeartbeat(node *model.CloudNode) *pb.CloudNode {
	pbNode := cloudNodeModelToPB(node)
	if pbNode == nil {
		return nil
	}
	if s.heartbeatStore != nil {
		var lastHB *time.Time
		if lastHeartbeat := s.heartbeatStore.GetLastHeartbeat(node.NodeID); lastHeartbeat != nil {
			lastHB = lastHeartbeat
			pbNode.LastHeartbeat = formatTime(lastHeartbeat)
		}
		pbNode.Status = calcNodeStatusText(lastHB, node.TimeoutThreshold)
		if liveVersion := s.heartbeatStore.GetRunningVersion(node.NodeID); liveVersion != "" {
			pbNode.RunningVersion = liveVersion
		}
	}
	return pbNode
}

// ========== 节点生命周期管理 ==========

// CreateNode 创建云节点（调用云厂商API）
func (s *ServiceImpl) CreateNode(ctx context.Context, node *pb.CloudNode, codeConfig *FunctionCodeConfig) (*pb.CloudNode, error) {
	if node.GetCloudAccountId() == "" {
		return nil, fmt.Errorf("cloud account id is required")
	}
	if node.GetRegion() == "" {
		return nil, fmt.Errorf("region is required")
	}
	if codeConfig == nil {
		return nil, fmt.Errorf("code config is required")
	}
	if codeConfig.Runtime == "" {
		return nil, fmt.Errorf("runtime is required")
	}

	hasCOSConfig := codeConfig.COSBucket != "" && codeConfig.COSPath != "" && codeConfig.COSRegion != ""
	hasZipFile := codeConfig.ZipFileBase64 != ""
	if !hasCOSConfig && !hasZipFile {
		return nil, fmt.Errorf("either COS config (bucket, path, region) or zip file is required")
	}

	nodeModel := cloudNodePBToModel(node)

	s.init()
	client := s.providerFactory.GetCloudProviderByAccount(nodeModel.CloudAccountID)
	if client == nil {
		return nil, fmt.Errorf("failed to get provider client for cloud account: %s", nodeModel.CloudAccountID)
	}

	nodeID, err := s.generateNodeID(ctx, nodeModel.Region, nodeModel.BizType)
	if err != nil {
		return nil, fmt.Errorf("failed to generate node id: %w", err)
	}
	nodeModel.NodeID = nodeID

	namespace, err := s.getAvailableNamespace(ctx, nodeModel.CloudAccountID, nodeModel.Region, client)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate namespace: %w", err)
	}
	nodeModel.Namespace = namespace

	if nodeModel.NodeType == "" {
		nodeModel.NodeType = model.NodeTypeSCFEvent
	}
	nodeModel.IsDeleted = common.IsDeletedFalse
	if nodeModel.SupportedCollectors == "" {
		nodeModel.SupportedCollectors = "[]"
	}
	if nodeModel.Metadata == "" {
		nodeModel.Metadata = "{}"
	}
	if nodeModel.Tag == "" {
		nodeModel.Tag = s.getRegionTagByAccount(ctx, nodeModel.CloudAccountID, nodeModel.Region)
	}

	req := buildCreateFunctionRequest(nodeModel, codeConfig)
	if hasCOSConfig {
		log.InfoContextf(ctx, "[CloudNode] Creating function with COS: bucket=%s, path=%s, region=%s, runtime=%s",
			codeConfig.COSBucket, codeConfig.COSPath, codeConfig.COSRegion, req.Runtime)
	} else {
		log.InfoContextf(ctx, "[CloudNode] Creating function with ZipFile: base64_length=%d, runtime=%s",
			len(codeConfig.ZipFileBase64), req.Runtime)
	}

	var funcInfo *provider.FunctionInfo
	err = common.RetryOperation(ctx, func() error {
		var createErr error
		funcInfo, createErr = client.CreateFunction(ctx, req)
		return createErr
	}, fmt.Sprintf("CreateFunction(%s)", nodeModel.NodeID))
	if err != nil {
		return nil, fmt.Errorf("failed to create function: %w", err)
	}

	log.InfoContextf(ctx, "[CloudNode] Successfully created cloud function: %s (ID: %s, cls_topic_id: %s)",
		funcInfo.FunctionName, funcInfo.FunctionID, funcInfo.ClsTopicID)

	if funcInfo.ClsTopicID != "" {
		nodeModel.ClsTopicID = funcInfo.ClsTopicID
	}

	if nodeModel.NodeType == model.NodeTypeSCFWeb {
		if err := s.createFunctionURL(ctx, client, nodeModel); err != nil {
			log.WarnContextf(ctx, "[CloudNode] Failed to create function URL for %s: %v", nodeModel.NodeID, err)
		}
	}

	return cloudNodeModelToPB(nodeModel), nil
}

// buildCreateFunctionRequest 构建云厂商创建函数请求。
func buildCreateFunctionRequest(node *model.CloudNode, codeConfig *FunctionCodeConfig) *provider.CreateFunctionRequest {
	req := &provider.CreateFunctionRequest{
		Region:       node.Region,
		FunctionName: node.NodeID,
		Runtime:      codeConfig.Runtime,
		Handler:      codeConfig.Handler,
		Namespace:    node.Namespace,
		Description:  "MooX Created",
		FunctionType: model.SCFFunctionType(node.NodeType),
		MemorySize:   128,
		Timeout:      30,
		Environment:  cloneStringMap(codeConfig.Environment),
	}
	if codeConfig.COSBucket != "" && codeConfig.COSPath != "" && codeConfig.COSRegion != "" {
		req.COSBucket = codeConfig.COSBucket
		req.COSPath = codeConfig.COSPath
		req.COSRegion = codeConfig.COSRegion
	} else {
		req.ZipFile = codeConfig.ZipFileBase64
	}
	return req
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// createFunctionURL 为scf-web类型函数创建函数URL触发器
func (s *ServiceImpl) createFunctionURL(ctx context.Context, client provider.Client, node *model.CloudNode) error {
	triggerDesc := `{"AuthType":"NONE","NetConfig":{"EnableIntranet":true,"EnableExtranet":true}}`
	req := &provider.CreateTriggerRequest{
		Region:       node.Region,
		FunctionName: node.NodeID,
		TriggerName:  "func_url",
		TriggerType:  "http",
		TriggerDesc:  triggerDesc,
		Namespace:    node.Namespace,
		Enable:       true,
		Description:  "MooX Auto Created Function URL",
	}

	err := common.RetryOperation(ctx, func() error {
		return client.CreateTrigger(ctx, req)
	}, fmt.Sprintf("CreateFunctionURL(%s)", node.NodeID))
	if err != nil {
		return fmt.Errorf("create function URL trigger failed: %w", err)
	}

	log.InfoContextf(ctx, "[CloudNode] Successfully created function URL for: %s", node.NodeID)
	s.saveFunctionURLToMetadata(ctx, client, node)
	return nil
}

// saveFunctionURLToMetadata 查询函数URL触发器信息并保存到节点Metadata
func (s *ServiceImpl) saveFunctionURLToMetadata(ctx context.Context, client provider.Client, node *model.CloudNode) {
	triggers, err := client.ListTriggers(ctx, node.NodeID, node.Namespace, node.Region)
	if err != nil {
		log.WarnContextf(ctx, "[CloudNode] Failed to list triggers for %s: %v", node.NodeID, err)
		return
	}

	for _, trigger := range triggers {
		if trigger.TriggerType == "http" {
			var metadata map[string]interface{}
			if err := json.Unmarshal([]byte(node.Metadata), &metadata); err != nil {
				metadata = make(map[string]interface{})
			}
			metadata["function_url_trigger"] = trigger.TriggerDesc

			metadataBytes, err := json.Marshal(metadata)
			if err != nil {
				log.WarnContextf(ctx, "[CloudNode] Failed to marshal metadata: %v", err)
				return
			}
			node.Metadata = string(metadataBytes)
			log.InfoContextf(ctx, "[CloudNode] Saved function URL to metadata for: %s", node.NodeID)
			return
		}
	}
}

// UpdateNode 更新云节点
func (s *ServiceImpl) UpdateNode(ctx context.Context, node *pb.CloudNode) error {
	if node.GetNodeId() == "" {
		return fmt.Errorf("node ID is required")
	}

	existing, err := s.nodeDAO.GetCloudNode(ctx, node.GetNodeId())
	if err != nil {
		return fmt.Errorf("failed to check existing node: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("node not found")
	}

	// 在已有节点基础上，仅覆盖前端传入的非零值字段
	if node.GetCloudAccountId() != "" {
		existing.CloudAccountID = node.GetCloudAccountId()
	}
	if node.GetNamespace() != "" {
		existing.Namespace = node.GetNamespace()
	}
	if node.GetNodeType() != "" {
		existing.NodeType = node.GetNodeType()
	}
	if node.GetBizType() != "" {
		existing.BizType = node.GetBizType()
	}
	if node.GetRegion() != "" {
		existing.Region = node.GetRegion()
	}
	if node.GetTag() != "" {
		existing.Tag = node.GetTag()
	}
	if node.GetIpAddress() != "" {
		existing.IPAddress = node.GetIpAddress()
	}
	if node.GetSupportedCollectors() != "" {
		existing.SupportedCollectors = node.GetSupportedCollectors()
	}
	if node.GetMetadata() != "" {
		existing.Metadata = node.GetMetadata()
	}
	if node.GetProbeUrl() != "" {
		existing.ProbeURL = node.GetProbeUrl()
	}

	// 数值和布尔字段始终覆盖
	existing.TimeoutThreshold = int(node.GetTimeoutThreshold())
	existing.HeartbeatInterval = int(node.GetHeartbeatInterval())
	existing.ProbeEnabled = node.GetProbeEnabled()

	if err := s.validateJSONField(existing.SupportedCollectors, "supported_collectors"); err != nil {
		return err
	}
	if err := s.validateJSONField(existing.Metadata, "metadata"); err != nil {
		return err
	}

	if existing.Region != node.GetRegion() && node.GetRegion() != "" || existing.Tag == "" {
		existing.Tag = s.getRegionTagByAccount(ctx, existing.CloudAccountID, existing.Region)
	}

	if err := s.nodeDAO.UpdateCloudNode(ctx, existing); err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}
	log.InfoContextf(ctx, "[CloudNode] Successfully updated node %s", node.GetNodeId())
	return nil
}

// DeleteNode 删除云节点并调用云厂商API删除云函数
func (s *ServiceImpl) DeleteNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}

	s.init()
	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	if client == nil {
		return fmt.Errorf("failed to get provider client for cloud account: %s", node.CloudAccountID)
	}

	deleteErr := common.RetryOperation(ctx, func() error {
		return client.DeleteFunction(ctx, node.NodeID, node.Namespace, node.Region)
	}, fmt.Sprintf("DeleteFunction(%s)", node.NodeID))

	if deleteErr != nil {
		log.WarnContextf(ctx, "[CloudNode] Failed to delete cloud function %s: %v", node.NodeID, deleteErr)
	} else {
		log.InfoContextf(ctx, "[CloudNode] Successfully deleted cloud function: %s", node.NodeID)
	}
	return nil
}

// DeleteNodeFromDB 从数据库删除节点记录
func (s *ServiceImpl) DeleteNodeFromDB(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if err := s.nodeDAO.DeleteCloudNode(ctx, nodeID); err != nil {
		return fmt.Errorf("failed to delete node from database: %w", err)
	}
	log.InfoContextf(ctx, "[CloudNode] Successfully deleted node %s from database", nodeID)
	return nil
}

// saveNodeToDB 保存节点信息到数据库（由 executor 调用）。
func (s *ServiceImpl) saveNodeToDB(ctx context.Context, node *pb.CloudNode) error {
	if node == nil || node.GetNodeId() == "" {
		return fmt.Errorf("NodeID is required")
	}
	if err := s.nodeDAO.CreateCloudNode(ctx, cloudNodePBToModel(node)); err != nil {
		return fmt.Errorf("failed to save node to database: %w", err)
	}
	log.InfoContextf(ctx, "[CloudNode] Successfully saved node %s to database", node.GetNodeId())
	return nil
}

// ========== 节点状态管理 ==========

// UpdateNodePackageID 更新节点代码包ID
func (s *ServiceImpl) UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if packageID == "" {
		return fmt.Errorf("package ID is required")
	}
	if err := s.nodeDAO.UpdateNodePackageID(ctx, nodeID, packageID); err != nil {
		return fmt.Errorf("failed to update node package ID: %w", err)
	}
	log.InfoContextf(ctx, "[CloudNode] Successfully updated node %s package ID to %s", nodeID, packageID)
	return nil
}

// ========== 节点部署 ==========

// DeployNode 部署/更新云节点
func (s *ServiceImpl) DeployNode(ctx context.Context, nodeID string, codeConfig *FunctionCodeConfig) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if codeConfig == nil {
		return fmt.Errorf("code config is required")
	}

	hasCOSConfig := codeConfig.COSBucket != "" && codeConfig.COSPath != "" && codeConfig.COSRegion != ""
	hasZipFile := codeConfig.ZipFileBase64 != ""
	if !hasCOSConfig && !hasZipFile {
		return fmt.Errorf("either COS config (bucket, path, region) or zip file is required")
	}

	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	s.init()
	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	if client == nil {
		return fmt.Errorf("failed to get provider client for cloud account: %s", node.CloudAccountID)
	}

	var description *string
	if codeConfig.Version != "" {
		desc := fmt.Sprintf("MooX Deployed - %s", codeConfig.Version)
		description = &desc
	}

	req := &provider.UpdateFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
		Region:       node.Region,
		Description:  description,
	}

	if hasCOSConfig {
		req.COSBucket = codeConfig.COSBucket
		req.COSPath = codeConfig.COSPath
		req.COSRegion = codeConfig.COSRegion
		log.InfoContextf(ctx, "[CloudNode] Deploying function with COS: bucket=%s, path=%s, region=%s, version=%s",
			codeConfig.COSBucket, codeConfig.COSPath, codeConfig.COSRegion, codeConfig.Version)
	} else {
		req.ZipFile = codeConfig.ZipFileBase64
		log.InfoContextf(ctx, "[CloudNode] Deploying function with ZipFile: base64_length=%d, version=%s",
			len(codeConfig.ZipFileBase64), codeConfig.Version)
	}

	err = common.RetryOperation(ctx, func() error {
		return client.UpdateFunction(ctx, req)
	}, fmt.Sprintf("DeployNode(%s)", nodeID))
	if err != nil {
		return fmt.Errorf("failed to deploy function: %w", err)
	}

	if err := s.syncClsTopicFromCloud(ctx, client, node); err != nil {
		log.WarnContextf(ctx, "[CloudNode] Failed to sync cls_topic_id after deploy %s: %v", nodeID, err)
	}

	log.InfoContextf(ctx, "[CloudNode] Successfully deployed function to node: %s", nodeID)
	return nil
}

// syncClsTopicFromCloud 从云厂商 GetFunction 同步 CLS TopicId 到本地节点记录。
func (s *ServiceImpl) syncClsTopicFromCloud(ctx context.Context, client provider.Client, node *model.CloudNode) error {
	if node == nil || client == nil {
		return nil
	}
	info, err := client.GetFunction(ctx, node.NodeID, node.Namespace, node.Region)
	if err != nil {
		return fmt.Errorf("get function: %w", err)
	}
	if info == nil || info.ClsTopicID == "" {
		return fmt.Errorf("cls_topic_id empty for function %s", node.NodeID)
	}
	if err := s.nodeDAO.UpdateClsTopicID(ctx, node.NodeID, info.ClsTopicID); err != nil {
		return err
	}
	node.ClsTopicID = info.ClsTopicID
	log.InfoContextf(ctx, "[CloudNode] Synced cls_topic_id for %s: %s", node.NodeID, info.ClsTopicID)
	return nil
}

// ========== 云函数调用 ==========

// InvokeFunction 调用云函数
func (s *ServiceImpl) InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (*pb.InvokeFunctionRsp, error) {
	s.init()
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return nil, fmt.Errorf("node not found: %s", nodeID)
	}

	cloudProvider := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	resp, err := cloudProvider.InvokeFunction(ctx, &provider.InvokeFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
		Region:       node.Region,
		EventData:    eventData,
		InvokeType:   InvokeTypeSync,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to invoke function: %w", err)
	}

	return &pb.InvokeFunctionRsp{
		Code:      int32(resp.StatusCode),
		Message:   resp.ErrorMessage,
		RequestId: resp.RequestID,
		Result:    interfaceToStruct(resp.ReturnResult),
		Duration:  resp.Duration,
	}, nil
}
