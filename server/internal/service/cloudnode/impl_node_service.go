package cloudnode

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 节点查询 ==========

// GetNodeList 获取云节点列表（支持分页）
func (s *ServiceImpl) GetNodeList(ctx context.Context, req *NodeListRequest) (*NodeListResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}

	// 构建DAO查询参数
	query := &dao.NodeListQuery{
		Page:           page,
		PageSize:       pageSize,
		NodeID:         req.NodeID,
		CloudAccountID: req.CloudAccountID,
		Namespace:      req.Namespace,
		Region:         req.Region,
		NodeType:       req.NodeType,
		BizType:        req.BizType,
		Tag:            req.Tag,
		Status:         req.Status,
		Keyword:        req.Keyword,
	}

	// 如果有状态过滤，需要获取在线节点ID列表传给DAO
	if req.Status != "" {
		query.OnlineNodeIDs = s.heartbeatStore.GetOnlineNodeIDs()
	}

	// 查询数据
	nodes, total, err := s.nodeDAO.GetCloudNodeList(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get node list: %w", err)
	}

	// 转换为DTO并合并心跳数据
	items := make([]*CloudNodeDTO, len(nodes))
	for i, node := range nodes {
		items[i] = s.convertToCloudNodeDTOWithHeartbeat(node)
	}

	return &NodeListResponse{
		Total: total,
		Items: items,
		Page:  page,
		Size:  len(items),
	}, nil
}

// GetCloudNode 根据节点ID获取云节点详情
func (s *ServiceImpl) GetCloudNode(ctx context.Context, nodeID string) (*CloudNodeDTO, error) {
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

	return s.convertToCloudNodeDTOWithHeartbeat(node), nil
}

// GetNodesByType 根据节点类型获取云节点列表
func (s *ServiceImpl) GetNodesByType(ctx context.Context, nodeType string) ([]*CloudNodeDTO, error) {
	nodes, err := s.nodeDAO.GetCloudNodesByType(ctx, nodeType)
	if err != nil {
		return nil, err
	}
	result := make([]*CloudNodeDTO, len(nodes))
	for i, node := range nodes {
		result[i] = s.convertToCloudNodeDTOWithHeartbeat(node)
	}
	return result, nil
}

// GetOnlineNodes 获取所有在线云节点列表
// 先从内存获取在线节点ID列表，再查询节点详情
func (s *ServiceImpl) GetOnlineNodes(ctx context.Context) ([]*CloudNodeDTO, error) {
	// 从内存获取在线节点ID列表
	onlineNodeIDs := s.heartbeatStore.GetOnlineNodeIDs()
	if len(onlineNodeIDs) == 0 {
		return []*CloudNodeDTO{}, nil
	}

	// 根据ID列表查询节点
	nodes, err := s.nodeDAO.GetNodesByIDs(ctx, onlineNodeIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get online nodes: %w", err)
	}

	result := make([]*CloudNodeDTO, len(nodes))
	for i, node := range nodes {
		result[i] = s.convertToCloudNodeDTOWithHeartbeat(node)
	}
	return result, nil
}

// ========== 节点生命周期管理 ==========

// CreateNode 创建云节点（调用云厂商API）
func (s *ServiceImpl) CreateNode(ctx context.Context, node *CloudNodeDTO, codeConfig *FunctionCodeConfig) (*CloudNodeDTO, error) {
	// 验证必填字段
	if node.CloudAccountID == "" {
		return nil, fmt.Errorf("cloud account id is required")
	}
	if node.Region == "" {
		return nil, fmt.Errorf("region is required")
	}
	if codeConfig == nil {
		return nil, fmt.Errorf("code config is required")
	}

	// 验证代码配置：必须提供COS配置或ZipFile，优先使用COS
	hasCOSConfig := codeConfig.COSBucket != "" && codeConfig.COSPath != "" && codeConfig.COSRegion != ""
	hasZipFile := codeConfig.ZipFileBase64 != ""

	if !hasCOSConfig && !hasZipFile {
		return nil, fmt.Errorf("either COS config (bucket, path, region) or zip file is required")
	}

	// 转换为Model
	nodeModel := cloudNodeDTOToModel(node)

	// 初始化provider factory
	s.init()

	// 获取云厂商客户端
	client := s.providerFactory.GetCloudProviderByAccount(nodeModel.CloudAccountID)
	if client == nil {
		return nil, fmt.Errorf("failed to get provider client for cloud account: %s", nodeModel.CloudAccountID)
	}

	// 生成NodeID（作为云函数名）
	nodeID, err := s.generateNodeID(ctx, nodeModel.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to generate node id: %w", err)
	}
	nodeModel.NodeID = nodeID

	// 分配Namespace
	namespace, err := s.getAvailableNamespace(ctx, nodeModel.CloudAccountID, nodeModel.Region, client)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate namespace: %w", err)
	}
	nodeModel.Namespace = namespace

	// 设置默认值
	if nodeModel.NodeType == "" {
		nodeModel.NodeType = model.NodeTypeSCF
	}
	nodeModel.Invalid = model.InvalidNo
	if nodeModel.SupportedCollectors == "" {
		nodeModel.SupportedCollectors = "[]"
	}
	if nodeModel.Metadata == "" {
		nodeModel.Metadata = "{}"
	}

	// 根据 region 自动设置 tag（国内/海外）
	if nodeModel.Tag == "" {
		nodeModel.Tag = s.getRegionTagByAccount(ctx, nodeModel.CloudAccountID, nodeModel.Region)
	}

	// 调用云厂商API创建云函数（带重试机制）
	// 优先使用COS方式，如果没有COS配置则使用ZipFile方式
	runtime := codeConfig.Runtime
	if runtime == "" {
		runtime = "Go1" // 默认使用Go1
		log.WarnContextf(ctx, "[CloudNode] Runtime not specified in codeConfig, using default: %s", runtime)
	}

	req := &provider.CreateFunctionRequest{
		Region:       nodeModel.Region,
		FunctionName: nodeModel.NodeID,
		Runtime:      runtime,
		Namespace:    nodeModel.Namespace,
		Description:  "MooX Created",
		MemorySize:   128, // 默认128MB
		Timeout:      30,  // 默认30秒
		Environment:  map[string]string{},
	}

	if hasCOSConfig {
		// 使用COS方式创建
		req.COSBucket = codeConfig.COSBucket
		req.COSPath = codeConfig.COSPath
		req.COSRegion = codeConfig.COSRegion
		log.InfoContextf(ctx, "[CloudNode] Creating function with COS: bucket=%s, path=%s, region=%s, runtime=%s",
			codeConfig.COSBucket, codeConfig.COSPath, codeConfig.COSRegion, runtime)
	} else {
		// 使用ZipFile方式创建
		req.ZipFile = codeConfig.ZipFileBase64
		log.InfoContextf(ctx, "[CloudNode] Creating function with ZipFile: base64_length=%d, runtime=%s",
			len(codeConfig.ZipFileBase64), runtime)
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

	log.InfoContextf(ctx, "[CloudNode] Successfully created cloud function: %s (ID: %s)", funcInfo.FunctionName, funcInfo.FunctionID)

	// 转换回DTO返回
	return s.ConvertToCloudNodeDTO(nodeModel), nil
}

// UpdateNode 更新云节点
func (s *ServiceImpl) UpdateNode(ctx context.Context, node *CloudNodeDTO) error {
	if node.NodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 检查节点是否存在
	existing, err := s.nodeDAO.GetCloudNode(ctx, node.NodeID)
	if err != nil {
		return fmt.Errorf("failed to check existing node: %w", err)
	}

	if existing == nil {
		return fmt.Errorf("node not found")
	}

	// 验证JSON字段
	if err := s.validateJSONField(node.SupportedCollectors, "supported_collectors"); err != nil {
		return err
	}

	if err := s.validateJSONField(node.Metadata, "metadata"); err != nil {
		return err
	}

	// 转换为Model
	nodeModel := cloudNodeDTOToModel(node)

	// 如果 region 发生变化，或 tag 为空，则自动更新 tag
	if nodeModel.Region != existing.Region || nodeModel.Tag == "" {
		nodeModel.Tag = s.getRegionTagByAccount(ctx, existing.CloudAccountID, nodeModel.Region)
	}

	if err := s.nodeDAO.UpdateCloudNode(ctx, nodeModel); err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}
	log.InfoContextf(ctx, "[CloudNode] Successfully updated node %s", node.NodeID)
	return nil
}

// DeleteNode 删除云节点并调用云厂商API删除云函数
func (s *ServiceImpl) DeleteNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}

	// 初始化provider factory
	s.init()

	// 获取云厂商客户端
	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	if client == nil {
		return fmt.Errorf("failed to get provider client for cloud account: %s", node.CloudAccountID)
	}

	// 调用云厂商API删除云函数（带重试机制）
	deleteErr := common.RetryOperation(ctx, func() error {
		return client.DeleteFunction(ctx, node.NodeID, node.Namespace, node.Region)
	}, fmt.Sprintf("DeleteFunction(%s)", node.NodeID))

	if deleteErr != nil {
		log.WarnContextf(ctx, "[CloudNode] Failed to delete cloud function %s: %v", node.NodeID, deleteErr)
		// 即使云函数删除失败，也返回成功（数据库删除由executor处理）
	} else {
		log.InfoContextf(ctx, "[CloudNode] Successfully deleted cloud function: %s", node.NodeID)
	}

	// 不在此处删除数据库记录，由executor处理
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

// ========== 节点数据库操作 ==========

// saveNodeToDB 保存节点信息到数据库（私有方法）
func (s *ServiceImpl) saveNodeToDB(ctx context.Context, node *CloudNodeDTO) error {
	if node == nil {
		return fmt.Errorf("node is nil")
	}
	if node.NodeID == "" {
		return fmt.Errorf("NodeID is required")
	}

	// 转换为model
	nodeModel := cloudNodeDTOToModel(node)

	// 保存节点到数据库
	if err := s.nodeDAO.CreateCloudNode(ctx, nodeModel); err != nil {
		return fmt.Errorf("failed to save node to database: %w", err)
	}

	log.InfoContextf(ctx, "[CloudNode] Successfully saved node %s to database", node.NodeID)
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

	// 验证代码配置：必须提供COS配置或ZipFile，优先使用COS
	hasCOSConfig := codeConfig.COSBucket != "" && codeConfig.COSPath != "" && codeConfig.COSRegion != ""
	hasZipFile := codeConfig.ZipFileBase64 != ""

	if !hasCOSConfig && !hasZipFile {
		return fmt.Errorf("either COS config (bucket, path, region) or zip file is required")
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	// 初始化provider factory
	s.init()

	// 获取云厂商客户端
	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	if client == nil {
		return fmt.Errorf("failed to get provider client for cloud account: %s", node.CloudAccountID)
	}

	// 调用云厂商API更新云函数代码（带重试机制）
	// 优先使用COS方式，如果没有COS配置则使用ZipFile方式

	// 构建描述信息，包含版本号
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
		// 使用COS方式部署
		req.COSBucket = codeConfig.COSBucket
		req.COSPath = codeConfig.COSPath
		req.COSRegion = codeConfig.COSRegion
		log.InfoContextf(ctx, "[CloudNode] Deploying function with COS: bucket=%s, path=%s, region=%s, version=%s",
			codeConfig.COSBucket, codeConfig.COSPath, codeConfig.COSRegion, codeConfig.Version)
	} else {
		// 使用ZipFile方式部署
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

	log.InfoContextf(ctx, "[CloudNode] Successfully deployed function to node: %s", nodeID)
	return nil
}

// ========== 云函数调用 ==========

// InvokeFunction 调用云函数
func (s *ServiceImpl) InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (*InvokeFunctionResponse, error) {
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

	return &InvokeFunctionResponse{
		RequestID:    resp.RequestID,
		Result:       resp.Result,
		Duration:     resp.Duration,
		StatusCode:   resp.StatusCode,
		ErrorMessage: resp.ErrorMessage,
		ErrorType:    resp.ErrorType,
		ReturnResult: resp.ReturnResult,
	}, nil
}
