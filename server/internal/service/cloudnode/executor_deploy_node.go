package cloudnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"trpc.group/trpc-go/trpc-go/log"
)

// DeployNodeExecutor 单节点部署执行器
type DeployNodeExecutor struct {
	cloudNodeService Service
}

// NewDeployNodeExecutor 创建单节点部署执行器
func NewDeployNodeExecutor(
	cloudNodeService Service,
) *DeployNodeExecutor {
	return &DeployNodeExecutor{
		cloudNodeService: cloudNodeService,
	}
}

// NodeDeployItem 节点部署项
type NodeDeployItem struct {
	NodeID    string `json:"node_id"`
	PackageID string `json:"package_id"`
}

// Name 返回执行器外显名称
func (e *DeployNodeExecutor) Name() string {
	return "部署节点"
}

// Type 返回执行器类型
func (e *DeployNodeExecutor) Type() string {
	return asynctask.TaskTypeDeployNode
}

// ValidateRequest 验证请求
func (e *DeployNodeExecutor) ValidateRequest(taskData string) error {
	var deployItem NodeDeployItem
	if err := json.Unmarshal([]byte(taskData), &deployItem); err != nil {
		return fmt.Errorf("invalid request format: %w", err)
	}

	if deployItem.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if deployItem.PackageID == "" {
		return fmt.Errorf("package_id is required")
	}

	return nil
}

// Execute 执行单个节点部署任务
func (e *DeployNodeExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
	// 解析请求参数
	var deployItem NodeDeployItem
	if err := json.Unmarshal([]byte(requestParams), &deployItem); err != nil {
		return "", fmt.Errorf("failed to parse task data: %w", err)
	}

	log.InfoContextf(ctx, "[DeployNodeExecutor] Deploying node: TaskID=%s, NodeID=%s, PackageID=%s",
		taskID, deployItem.NodeID, deployItem.PackageID)

	// 获取代码包详情
	pkgDTO, err := e.cloudNodeService.GetPackageDetail(ctx, deployItem.PackageID)
	if err != nil {
		errorMsg := fmt.Sprintf("获取代码包信息失败: %v", err)
		log.ErrorContextf(ctx, "[DeployNodeExecutor] %s", errorMsg)
		return "", fmt.Errorf("%s", errorMsg)
	}

	// 转换为packageInfo
	pkg := dtoToPackageInfo(pkgDTO)

	// 准备函数代码
	codeConfig, err := e.prepareFunctionCode(ctx, pkg)
	if err != nil {
		errorMsg := fmt.Sprintf("准备函数代码失败: %v", err)
		log.ErrorContextf(ctx, "[DeployNodeExecutor] %s", errorMsg)
		return "", fmt.Errorf("%s", errorMsg)
	}

	// 验证代码信息
	if !e.isCodeConfigValid(codeConfig) {
		errorMsg := "函数代码信息不完整"
		log.ErrorContextf(ctx, "[DeployNodeExecutor] %s", errorMsg)
		return "", fmt.Errorf("%s", errorMsg)
	}

	// 调用云厂商API部署函数（支持COS和ZipFile两种方式，优先COS）
	if err := e.cloudNodeService.DeployNode(ctx, deployItem.NodeID, codeConfig); err != nil {
		errorMsg := fmt.Sprintf("部署函数失败: %v", err)
		log.ErrorContextf(ctx, "[DeployNodeExecutor] %s", errorMsg)
		return "", fmt.Errorf("%s", errorMsg)
	}

	log.InfoContextf(ctx, "[DeployNodeExecutor] Successfully deployed function to node %s; TaskID:%s, Package:%s",
		deployItem.NodeID, taskID, deployItem.PackageID)

	// 更新节点的package_id
	e.updateNodePackageID(ctx, deployItem.NodeID, deployItem.PackageID)

	// 任务成功完成，返回JSON结果
	resultData := map[string]interface{}{
		"node_id":    deployItem.NodeID,
		"package_id": deployItem.PackageID,
		"version":    pkg.Version,
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result data: %w", err)
	}
	return string(resultJSON), nil
}

// packageInfo 代码包信息（简化版，用于executor）
type packageInfo struct {
	ID               int64
	Runtime          string
	Version          string
	Status           int
	COSBucket        string
	CloudAccountID   string
	COSPath          string
	COSRegion        string
	OriginalFilename string
}

// prepareFunctionCode 根据PackageID准备函数代码
func (e *DeployNodeExecutor) prepareFunctionCode(ctx context.Context, pkg *packageInfo) (*FunctionCodeConfig, error) {
	if pkg.Status != model.PackageStatusAvailable {
		return nil, fmt.Errorf("代码包状态不可用: %d", pkg.Status)
	}

	codeConfig := &FunctionCodeConfig{
		Runtime: pkg.Runtime,
		Version: pkg.Version,
	}

	// 判断是否为COS存储（COSBucket不是"local"且有完整的COS信息）
	if pkg.COSBucket != "local" && pkg.CloudAccountID != "" && pkg.COSBucket != "" && pkg.COSPath != "" && pkg.COSRegion != "" {
		log.InfoContextf(ctx, "[DeployNodeExecutor] 使用COS部署: bucket=%s, path=%s, region=%s, runtime=%s",
			pkg.COSBucket, pkg.COSPath, pkg.COSRegion, pkg.Runtime)
		codeConfig.COSBucket = pkg.COSBucket
		codeConfig.COSPath = pkg.COSPath
		codeConfig.COSRegion = pkg.COSRegion
		return codeConfig, nil
	}

	// 使用本地存储（COSBucket标记为"local"）
	if pkg.COSBucket == "local" {
		localPath := constants.GetPackageFilePath(pkg.ID, pkg.OriginalFilename)

		// 检查本地文件是否存在
		if _, err := os.Stat(localPath); err == nil {
			log.InfoContextf(ctx, "[DeployNodeExecutor] 使用本地存储文件: %s, runtime=%s", localPath, pkg.Runtime)
			zipBase64, err := e.prepareZipFile(ctx, localPath)
			if err != nil {
				return nil, fmt.Errorf("读取本地代码包文件失败: %w", err)
			}
			codeConfig.ZipFileBase64 = zipBase64
			return codeConfig, nil
		}

		// 本地文件不存在
		return nil, fmt.Errorf("本地文件不存在: %s", localPath)
	}

	// 如果既不是COS存储也不是本地存储，返回错误
	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s, CloudAccountID=%s", pkg.COSBucket, pkg.CloudAccountID)
}

// prepareZipFile 读取并编码ZIP文件
func (e *DeployNodeExecutor) prepareZipFile(ctx context.Context, zipFilePath string) (string, error) {
	zipData, err := os.ReadFile(zipFilePath)
	if err != nil {
		log.ErrorContextf(ctx, "[DeployNodeExecutor] 读取zip文件失败: %v", err)
		return "", fmt.Errorf("读取zip文件失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(zipData), nil
}

// isCodeConfigValid 验证代码配置是否有效
func (e *DeployNodeExecutor) isCodeConfigValid(codeConfig *FunctionCodeConfig) bool {
	// 本地上传方式：需要ZipFile
	if codeConfig.ZipFileBase64 != "" {
		return true
	}
	// COS部署方式：需要完整的COS信息
	return codeConfig.COSBucket != "" && codeConfig.COSPath != ""
}

// updateNodePackageID 更新节点记录中的package_id字段
func (e *DeployNodeExecutor) updateNodePackageID(ctx context.Context, nodeID string, packageID string) {
	if err := e.cloudNodeService.UpdateNodePackageID(ctx, nodeID, packageID); err != nil {
		log.ErrorContextf(ctx, "[DeployNodeExecutor] Failed to update package_id for node %s: %v", nodeID, err)
		return
	}
	log.InfoContextf(ctx, "[DeployNodeExecutor] Updated package_id %s for node %s", packageID, nodeID)
}

// dtoToPackageInfo 将PackageDetail转换为packageInfo
func dtoToPackageInfo(dto *PackageDetail) *packageInfo {
	if dto == nil {
		return nil
	}
	return &packageInfo{
		ID:               dto.ID,
		Runtime:          dto.Runtime,
		Version:          dto.Version,
		Status:           dto.Status,
		COSBucket:        dto.COSBucket,
		CloudAccountID:   dto.CloudAccountID,
		COSPath:          dto.COSPath,
		COSRegion:        dto.COSRegion,
		OriginalFilename: dto.OriginalFilename,
	}
}
