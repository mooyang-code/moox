package logic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	asynctask "github.com/mooyang-code/moox/server/internal/service/asynctask"
	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	packagemgrlogic "github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"
	packagemgrmodel "github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// NodeDeployInfo 节点部署信息
type NodeDeployInfo struct {
	NodeID    string `json:"node_id"`
	PackageID int64  `json:"package_id"` // 代码包ID，替代直接上传文件
}

// BatchDeployNodeRequest 批量部署节点请求
type BatchDeployNodeRequest struct {
	Nodes []NodeDeployInfo `json:"nodes"` // 节点部署信息列表
}

// FunctionCodeInfo 函数代码信息
type FunctionCodeInfo struct {
	ZipFile   string // base64编码的ZIP文件（用于本地上传）
	COSBucket string // COS桶名（用于COS部署）
	COSPath   string // COS路径（用于COS部署）
	COSRegion string // COS区域（用于COS部署）
	Runtime   string // 运行时环境
}

// BatchDeployNodeExecutor 批量部署节点执行器
type BatchDeployNodeExecutor struct {
	scfNodeService   cloudnodelogic.SCFNodeService
	asyncTaskService asynctask.Service
	packageService   *packagemgrlogic.FunctionPackageService
	db               *gorm.DB
}

// NewBatchDeployNodeExecutor 创建批量部署节点执行器
func NewBatchDeployNodeExecutor(db *gorm.DB, scfNodeService cloudnodelogic.SCFNodeService, asyncTaskService asynctask.Service, packageService *packagemgrlogic.FunctionPackageService) *BatchDeployNodeExecutor {
	return &BatchDeployNodeExecutor{
		scfNodeService:   scfNodeService,
		asyncTaskService: asyncTaskService,
		packageService:   packageService,
		db:               db,
	}
}

// GetTaskType 返回任务类型
func (e *BatchDeployNodeExecutor) GetTaskType() string {
	return asynctaskmodel.TaskTypeBatchDeployNode
}

// ValidateRequest 验证任务请求
func (e *BatchDeployNodeExecutor) ValidateRequest(taskData string) error {
	log.Infof("BatchDeployNodeExecutor.ValidateRequest - taskData: %s", taskData)

	var request BatchDeployNodeRequest
	if err := json.Unmarshal([]byte(taskData), &request); err != nil {
		log.Errorf("Failed to unmarshal request: %v, taskData: %s", err, taskData)
		return fmt.Errorf("invalid request format: %w", err)
	}

	log.Infof("Parsed request - nodes count: %d", len(request.Nodes))

	if len(request.Nodes) == 0 {
		return fmt.Errorf("no nodes to deploy")
	}

	// 验证每个节点的部署信息
	for i, node := range request.Nodes {
		if node.NodeID == "" {
			return fmt.Errorf("node[%d]: node_id is required", i)
		}
		if node.PackageID <= 0 {
			return fmt.Errorf("node[%d]: package_id is required and must be positive", i)
		}
	}

	return nil
}

// Execute 执行批量部署任务
func (e *BatchDeployNodeExecutor) Execute(ctx context.Context, task *asynctaskmodel.AsyncTask) error {
	log.InfoContextf(ctx, "Starting batch deploy node task: %s", task.TaskID)

	// 解析请求参数
	var request BatchDeployNodeRequest
	if err := json.Unmarshal([]byte(task.RequestParams), &request); err != nil {
		errorMsg := fmt.Sprintf("failed to parse request params: %v", err)
		e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, nil, errorMsg)
		return fmt.Errorf(errorMsg)
	}

	// 创建任务详情
	var taskItems []asynctask.TaskItem
	for _, node := range request.Nodes {
		taskItems = append(taskItems, asynctask.TaskItem{
			ItemID:   node.NodeID,
			ItemName: fmt.Sprintf("Deploy Package %d to Node %s", node.PackageID, node.NodeID),
		})
	}

	if err := e.asyncTaskService.BatchCreateTaskDetails(ctx, task.TaskID, taskItems); err != nil {
		log.ErrorContextf(ctx, "Failed to create task details: %v", err)
	}

	// 将节点部署任务加入队列
	enqueuedCount := 0
	failedToEnqueueCount := 0

	for _, node := range request.Nodes {
		// 更新任务详情状态为处理中
		e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
			asynctaskmodel.TaskDetailStatusProcessing, "")

		// 检查包管理服务是否可用
		if e.packageService == nil {
			failedToEnqueueCount++
			errorMsg := "包管理服务未初始化，CloudProvider未设置或不支持COS功能"
			log.ErrorContextf(ctx, "Package service not available for node %s", node.NodeID)

			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
			continue
		}

		// 从包管理系统获取代码包信息
		pkg, err := e.packageService.GetPackageDetailModel(ctx, node.PackageID)
		if err != nil {
			failedToEnqueueCount++
			errorMsg := fmt.Sprintf("获取代码包信息失败: %v", err)
			log.ErrorContextf(ctx, "Failed to get package %d for node %s: %v", node.PackageID, node.NodeID, err)

			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
			continue
		}

		// 根据PackageID准备函数代码
		codeInfo, err := e.prepareFunctionCode(ctx, pkg)
		if err != nil {
			failedToEnqueueCount++
			errorMsg := fmt.Sprintf("准备函数代码失败: %v", err)
			log.ErrorContextf(ctx, "Failed to prepare function code for %s: %v", node.NodeID, err)

			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
			continue
		}

		// 验证代码信息是否完整
		if codeInfo.ZipFile == "" && (codeInfo.COSBucket == "" || codeInfo.COSPath == "") {
			failedToEnqueueCount++
			errorMsg := "函数代码信息不完整"
			log.ErrorContextf(ctx, "Function code info incomplete for %s", node.NodeID)

			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
			continue
		}

		// 将节点部署任务加入队列（实际部署将由Worker异步执行）
		err = e.scfNodeService.DeployToNodeWithPackage(ctx, node.NodeID, node.PackageID, task.TaskID)
		if err != nil {
			failedToEnqueueCount++
			errorMsg := fmt.Sprintf("将节点加入部署队列失败: %v", err)
			log.ErrorContextf(ctx, "Failed to enqueue node deployment for %s: %v", node.NodeID, err)

			// 如果无法加入队列，直接标记为失败
			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		} else {
			enqueuedCount++
			log.InfoContextf(ctx, "Successfully enqueued node %s for deployment; taskID:%s, package:%s-%s",
				node.NodeID, task.TaskID, pkg.PackageName, pkg.Version)

			// 更新节点记录中的package_id字段
			e.updateNodePackageID(ctx, node.NodeID, node.PackageID)

			// 注意：这里不再立即更新为成功状态，保持处理中状态
			// 实际的成功/失败状态将由NodeDeploymentWorker在部署完成后更新
		}
	}

	// 记录任务创建情况
	if failedToEnqueueCount == 0 {
		log.InfoContextf(ctx, "All nodes enqueued for deployment. Total: %d", len(request.Nodes))
	} else if enqueuedCount == 0 {
		log.ErrorContextf(ctx, "Failed to enqueue any nodes for deployment. Total: %d", len(request.Nodes))
		// 如果所有节点都无法加入队列，直接标记任务失败
		resultData := map[string]interface{}{
			"total_count":   len(request.Nodes),
			"success_count": 0,
			"failed_count":  failedToEnqueueCount,
		}
		return e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, resultData, "所有节点都无法加入部署队列")
	} else {
		log.WarnContextf(ctx, "Partially enqueued nodes for deployment. Total: %d, Enqueued: %d, Failed: %d",
			len(request.Nodes), enqueuedCount, failedToEnqueueCount)
	}

	// 任务已经提交到队列，等待Worker处理
	// 注意：这里不再调用CompleteTask，任务将保持处理中状态，直到所有Worker完成处理
	return nil
}

// updateNodePackageID 更新节点记录中的package_id字段
func (e *BatchDeployNodeExecutor) updateNodePackageID(ctx context.Context, nodeID string, packageID int64) {
	// 更新节点记录中的package_id
	err := e.db.Table("t_cloud_nodes").
		Where("c_node_id = ?", nodeID).
		Update("c_package_id", packageID).Error
	if err != nil {
		log.ErrorContextf(ctx, "Failed to update package_id for node %s: %v", nodeID, err)
	} else {
		log.InfoContextf(ctx, "Updated package_id %d for node %s", packageID, nodeID)
	}
}

// prepareFunctionCode 根据PackageID准备函数代码
func (e *BatchDeployNodeExecutor) prepareFunctionCode(ctx context.Context, pkg *packagemgrmodel.FunctionPackage) (*FunctionCodeInfo, error) {
	if pkg.Status != packagemgrmodel.PackageStatusAvailable {
		return nil, fmt.Errorf("代码包状态不可用: %d", pkg.Status)
	}

	// 判断是否为COS存储（COSBucket不是"local"且有完整的COS信息）
	if pkg.COSBucket != "local" && pkg.CloudAccountID != "" && pkg.COSBucket != "" && pkg.COSPath != "" && pkg.COSRegion != "" {
		log.InfoContextf(ctx, "[BatchDeployExecutor] 使用COS部署: bucket=%s, path=%s, region=%s, runtime=%s",
			pkg.COSBucket, pkg.COSPath, pkg.COSRegion, pkg.Runtime)
		return &FunctionCodeInfo{
			COSBucket: pkg.COSBucket,
			COSPath:   pkg.COSPath,
			COSRegion: pkg.COSRegion,
			Runtime:   pkg.Runtime,
		}, nil
	}

	// 使用本地存储（COSBucket标记为"local"）
	if pkg.COSBucket == "local" {
		localPath := constants.GetPackageFilePath(pkg.ID, pkg.OriginalFilename)

		// 检查本地文件是否存在
		if _, err := os.Stat(localPath); err == nil {
			log.InfoContextf(ctx, "[BatchDeployExecutor] 使用本地存储文件: %s, runtime=%s", localPath, pkg.Runtime)
			zipBase64, err := e.prepareZipFile(ctx, localPath)
			if err != nil {
				return nil, fmt.Errorf("读取本地代码包文件失败: %w", err)
			}
			return &FunctionCodeInfo{
				ZipFile: zipBase64,
				Runtime: pkg.Runtime,
			}, nil
		}

		// 本地文件不存在
		return nil, fmt.Errorf("本地文件不存在: %s", localPath)
	}

	// 如果既不是COS存储也不是本地存储，返回错误
	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s, CloudAccountID=%s", pkg.COSBucket, pkg.CloudAccountID)
}

// prepareZipFile 读取并编码ZIP文件
func (e *BatchDeployNodeExecutor) prepareZipFile(ctx context.Context, zipFilePath string) (string, error) {
	zipData, err := ioutil.ReadFile(zipFilePath)
	if err != nil {
		log.ErrorContextf(ctx, "[BatchDeployExecutor] 读取zip文件失败: %v", err)
		return "", fmt.Errorf("读取zip文件失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(zipData), nil
}
