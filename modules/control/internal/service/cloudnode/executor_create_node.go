package cloudnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"

	"trpc.group/trpc-go/trpc-go/log"
)

// CreateNodeExecutor 单节点创建执行器
type CreateNodeExecutor struct {
	cloudNodeService *ServiceImpl
}

// NewCreateNodeExecutor 创建单节点创建执行器
func NewCreateNodeExecutor(
	cloudNodeService *ServiceImpl,
) *CreateNodeExecutor {
	return &CreateNodeExecutor{
		cloudNodeService: cloudNodeService,
	}
}

// NodeCreateItem 节点创建项
type NodeCreateItem struct {
	CloudAccountID      string `json:"cloud_account_id"`
	NodeType            string `json:"node_type"`
	Runtime             string `json:"runtime"`
	BizType             string `json:"biz_type"`
	Region              string `json:"region"`
	IPAddress           string `json:"ip_address"`
	PackageID           string `json:"package_id"`
	SupportedCollectors string `json:"supported_collectors"`
	Metadata            string `json:"metadata"`
	TimeoutThreshold    int    `json:"timeout_threshold"`
	HeartbeatInterval   int    `json:"heartbeat_interval"`
	ProbeEnabled        *bool  `json:"probe_enabled"`
}

// Name 返回执行器外显名称
func (e *CreateNodeExecutor) Name() string {
	return "创建节点"
}

// Type 返回执行器类型
func (e *CreateNodeExecutor) Type() string {
	return asynctask.TaskTypeCreateNode
}

// Execute 执行单个节点创建任务 - 新TaskHandler签名
// 返回: resultData (JSON字符串), error
func (e *CreateNodeExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
	// 解析请求参数
	var nodeItem NodeCreateItem
	if err := json.Unmarshal([]byte(requestParams), &nodeItem); err != nil {
		return "", fmt.Errorf("failed to parse task data: %w", err)
	}

	log.InfoContextf(ctx, "[CreateNodeExecutor] Creating node: TaskID=%s, CloudAccountID=%s, Region=%s",
		taskID, nodeItem.CloudAccountID, nodeItem.Region)

	// 获取代码包配置
	codeConfig, err := e.getPackageCodeConfig(ctx, nodeItem.PackageID)
	if err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to get package config: %v", err)
		return "", fmt.Errorf("获取代码包配置失败: %w", err)
	}

	// 如果请求中指定了Runtime，则覆盖代码包中的Runtime
	if nodeItem.Runtime != "" {
		codeConfig.Runtime = nodeItem.Runtime
	}

	// 准备节点数据
	probeEnabled := true // 默认启用
	if nodeItem.ProbeEnabled != nil {
		probeEnabled = *nodeItem.ProbeEnabled
	}
	node := &model.CloudNode{
		CloudAccountID:      nodeItem.CloudAccountID,
		PackageID:           nodeItem.PackageID,
		NodeType:            nodeItem.NodeType,
		BizType:             nodeItem.BizType,
		Region:              nodeItem.Region,
		IPAddress:           nodeItem.IPAddress,
		SupportedCollectors: nodeItem.SupportedCollectors,
		Metadata:            nodeItem.Metadata,
		TimeoutThreshold:    nodeItem.TimeoutThreshold,
		HeartbeatInterval:   nodeItem.HeartbeatInterval,
		ProbeEnabled:        probeEnabled,
	}

	// 转换为DTO
	nodeDTO := modelToCloudNodeDTO(node)

	// 调用云厂商API创建节点
	createdNodeDTO, err := e.cloudNodeService.CreateNode(ctx, nodeDTO, codeConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to create cloud function: %v", err)
		return "", fmt.Errorf("创建云函数失败: %w", err)
	}

	// 更新node实例的NodeID（从CreateNode返回的DTO中获取）
	node.NodeID = createdNodeDTO.NodeID
	node.Namespace = createdNodeDTO.Namespace

	log.InfoContextf(ctx, "[CreateNodeExecutor] Generated NodeID: %s, Namespace: %s", node.NodeID, node.Namespace)

	// 保存节点到数据库
	if err := e.cloudNodeService.saveNodeToDB(ctx, createdNodeDTO); err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to save node to database: %v", err)
		return "", fmt.Errorf("保存节点到数据库失败: %w", err)
	}

	log.InfoContextf(ctx, "[CreateNodeExecutor] Node created successfully: NodeID=%s, TaskID=%s",
		node.NodeID, taskID)

	// 任务成功完成，返回JSON结果
	resultData := map[string]interface{}{
		"node_id": node.NodeID,
		"region":  node.Region,
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result data: %w", err)
	}
	return string(resultJSON), nil
}

// getPackageCodeConfig 获取代码包配置（支持本地存储和COS存储）
func (e *CreateNodeExecutor) getPackageCodeConfig(ctx context.Context, packageID string) (*FunctionCodeConfig, error) {
	if packageID == "" {
		return nil, fmt.Errorf("代码包ID为空")
	}

	// 获取代码包详情
	pkg, err := e.cloudNodeService.GetPackageDetail(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("获取代码包信息失败: %w", err)
	}

	if pkg.Status != model.PackageStatusAvailable {
		return nil, fmt.Errorf("代码包状态不可用: %d", pkg.Status)
	}

	codeConfig := &FunctionCodeConfig{
		Runtime: pkg.Runtime,
		Version: pkg.Version,
	}

	// 判断是否为COS存储（COSBucket不是"local"且有完整的COS信息）
	if pkg.COSBucket != "local" && pkg.COSBucket != "" && pkg.COSPath != "" && pkg.COSRegion != "" {
		// 使用COS方式
		codeConfig.COSBucket = pkg.COSBucket
		codeConfig.COSPath = pkg.COSPath
		codeConfig.COSRegion = pkg.COSRegion
		log.InfoContextf(ctx, "[CreateNodeExecutor] Using COS deployment: bucket=%s, path=%s, region=%s, runtime=%s, version=%s",
			pkg.COSBucket, pkg.COSPath, pkg.COSRegion, pkg.Runtime, pkg.Version)
		return codeConfig, nil
	}

	// 使用本地存储
	if pkg.COSBucket == "local" {
		localPath := constants.GetPackageFilePath(int64(pkg.ID), pkg.OriginalFilename)

		// 检查本地文件是否存在
		if _, err := os.Stat(localPath); err != nil {
			return nil, fmt.Errorf("本地文件不存在: %s", localPath)
		}

		// 读取文件并转换为Base64
		zipData, err := os.ReadFile(localPath)
		if err != nil {
			return nil, fmt.Errorf("读取文件失败: %w", err)
		}

		codeConfig.ZipFileBase64 = base64.StdEncoding.EncodeToString(zipData)
		log.InfoContextf(ctx, "[CreateNodeExecutor] Using local file deployment: path=%s, runtime=%s, version=%s",
			localPath, pkg.Runtime, pkg.Version)
		return codeConfig, nil
	}

	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s", pkg.COSBucket)
}

// modelToCloudNodeDTO 将model.CloudNode转换为CloudNodeDTO
func modelToCloudNodeDTO(node *model.CloudNode) *CloudNodeDTO {
	if node == nil {
		return nil
	}
	return &CloudNodeDTO{
		ID:                  node.ID,
		NodeID:              node.NodeID,
		CloudAccountID:      node.CloudAccountID,
		PackageID:           node.PackageID,
		Namespace:           node.Namespace,
		NodeType:            node.NodeType,
		BizType:             node.BizType,
		Region:              node.Region,
		IPAddress:           node.IPAddress,
		SupportedCollectors: node.SupportedCollectors,
		Metadata:            node.Metadata,
		TimeoutThreshold:    node.TimeoutThreshold,
		HeartbeatInterval:   node.HeartbeatInterval,
		ProbeEnabled:        node.ProbeEnabled,
		RunningVersion:      node.RunningVersion,
		Invalid:             node.Invalid,
		CreateTime:          node.CreateTime,
		ModifyTime:          node.ModifyTime,
	}
}
