package cloudnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

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
	CloudAccountID      string            `json:"cloud_account_id"`
	NodeType            string            `json:"node_type"`
	Runtime             string            `json:"runtime"`
	Handler             string            `json:"handler"`
	BizType             string            `json:"biz_type"`
	Config              map[string]string `json:"config"`
	Environment         map[string]string `json:"environment"`
	Region              string            `json:"region"`
	IPAddress           string            `json:"ip_address"`
	PackageID           string            `json:"package_id"`
	SupportedCollectors string            `json:"supported_collectors"`
	Metadata            string            `json:"metadata"`
	TimeoutThreshold    int               `json:"timeout_threshold"`
	HeartbeatInterval   int               `json:"heartbeat_interval"`
	ProbeEnabled        *bool             `json:"probe_enabled"`
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
	var nodeItem NodeCreateItem
	if err := json.Unmarshal([]byte(requestParams), &nodeItem); err != nil {
		return "", fmt.Errorf("failed to parse task data: %w", err)
	}

	log.InfoContextf(ctx, "[CreateNodeExecutor] Creating node: TaskID=%s, CloudAccountID=%s, Region=%s",
		taskID, nodeItem.CloudAccountID, nodeItem.Region)

	codeConfig, err := e.getPackageCodeConfig(ctx, nodeItem.PackageID)
	if err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to get package config: %v", err)
		return "", fmt.Errorf("获取代码包配置失败: %w", err)
	}
	applyNodeCreateItemToCodeConfig(codeConfig, nodeItem)

	probeEnabled := true
	if nodeItem.ProbeEnabled != nil {
		probeEnabled = *nodeItem.ProbeEnabled
	}
	node := &pb.CloudNode{
		CloudAccountId:      nodeItem.CloudAccountID,
		PackageId:           nodeItem.PackageID,
		NodeType:            nodeItem.NodeType,
		BizType:             nodeItem.BizType,
		Region:              nodeItem.Region,
		IpAddress:           nodeItem.IPAddress,
		SupportedCollectors: nodeItem.SupportedCollectors,
		Metadata:            nodeItem.Metadata,
		TimeoutThreshold:    int32(nodeItem.TimeoutThreshold),
		HeartbeatInterval:   int32(nodeItem.HeartbeatInterval),
		ProbeEnabled:        probeEnabled,
	}

	createdNode, err := e.cloudNodeService.CreateNode(ctx, node, codeConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to create cloud function: %v", err)
		return "", fmt.Errorf("创建云函数失败: %w", err)
	}

	log.InfoContextf(ctx, "[CreateNodeExecutor] Generated NodeID: %s, Namespace: %s", createdNode.GetNodeId(), createdNode.GetNamespace())

	if err := e.cloudNodeService.saveNodeToDB(ctx, createdNode); err != nil {
		log.ErrorContextf(ctx, "[CreateNodeExecutor] Failed to save node to database: %v", err)
		return "", fmt.Errorf("保存节点到数据库失败: %w", err)
	}

	log.InfoContextf(ctx, "[CreateNodeExecutor] Node created successfully: NodeID=%s, TaskID=%s",
		createdNode.GetNodeId(), taskID)

	resultData := map[string]interface{}{
		"node_id": createdNode.GetNodeId(),
		"region":  createdNode.GetRegion(),
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result data: %w", err)
	}
	return string(resultJSON), nil
}

func applyNodeCreateItemToCodeConfig(codeConfig *FunctionCodeConfig, nodeItem NodeCreateItem) {
	if nodeItem.Runtime != "" {
		codeConfig.Runtime = nodeItem.Runtime
	}
	if nodeItem.Handler != "" {
		codeConfig.Handler = nodeItem.Handler
	}
	merged := cloneStringMap(codeConfig.Environment)
	for key, value := range nodeItem.Config {
		merged[key] = value
	}
	for key, value := range nodeItem.Environment {
		merged[key] = value
	}
	if len(merged) > 0 {
		codeConfig.Environment = merged
	}
}

// getPackageCodeConfig 获取代码包配置（支持本地存储和COS存储）
func (e *CreateNodeExecutor) getPackageCodeConfig(ctx context.Context, packageID string) (*FunctionCodeConfig, error) {
	if packageID == "" {
		return nil, fmt.Errorf("代码包ID为空")
	}

	pkg, err := e.cloudNodeService.GetPackageDetail(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("获取代码包信息失败: %w", err)
	}
	if pkg.GetStatus() != int32(model.PackageStatusAvailable) {
		return nil, fmt.Errorf("代码包状态不可用: %d", pkg.GetStatus())
	}

	codeConfig := &FunctionCodeConfig{
		Runtime: pkg.GetRuntime(),
		Version: pkg.GetVersion(),
	}

	if pkg.GetCosBucket() != "local" && pkg.GetCosBucket() != "" && pkg.GetCosPath() != "" && pkg.GetCosRegion() != "" {
		codeConfig.COSBucket = pkg.GetCosBucket()
		codeConfig.COSPath = pkg.GetCosPath()
		codeConfig.COSRegion = pkg.GetCosRegion()
		log.InfoContextf(ctx, "[CreateNodeExecutor] Using COS deployment: bucket=%s, path=%s, region=%s, runtime=%s, version=%s",
			pkg.GetCosBucket(), pkg.GetCosPath(), pkg.GetCosRegion(), pkg.GetRuntime(), pkg.GetVersion())
		return codeConfig, nil
	}

	if pkg.GetCosBucket() == "local" {
		localPath := constants.GetPackageFilePath(pkg.GetId(), pkg.GetOriginalFilename())
		if _, err := os.Stat(localPath); err != nil {
			return nil, fmt.Errorf("本地文件不存在: %s", localPath)
		}
		zipData, err := os.ReadFile(localPath)
		if err != nil {
			return nil, fmt.Errorf("读取文件失败: %w", err)
		}
		codeConfig.ZipFileBase64 = base64.StdEncoding.EncodeToString(zipData)
		log.InfoContextf(ctx, "[CreateNodeExecutor] Using local file deployment: path=%s, runtime=%s, version=%s",
			localPath, pkg.GetRuntime(), pkg.GetVersion())
		return codeConfig, nil
	}

	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s", pkg.GetCosBucket())
}
