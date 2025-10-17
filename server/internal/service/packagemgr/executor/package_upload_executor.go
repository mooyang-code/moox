package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	asyncTaskLogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	asyncTaskModel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	cloudAccountModel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	packageModel "github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// PackageUploadRequest 代码包上传任务请求
type PackageUploadRequest struct {
	PackageID      int64  `json:"package_id"`       // 包ID
	CloudAccountID string `json:"cloud_account_id"` // 云账户ID
	COSBucket      string `json:"cos_bucket"`       // COS存储桶
	COSPath        string `json:"cos_path"`         // COS路径
	LocalFilePath  string `json:"local_file_path"`  // 本地文件路径
}

// PackageUploadExecutor 代码包上传执行器
type PackageUploadExecutor struct {
	db          *gorm.DB
	cosProvider provider.ClientWithCOS
	taskService asyncTaskLogic.AsyncTaskService
}

// NewPackageUploadExecutor 创建代码包上传执行器
func NewPackageUploadExecutor(db *gorm.DB, cosProvider provider.ClientWithCOS, taskService asyncTaskLogic.AsyncTaskService) *PackageUploadExecutor {
	return &PackageUploadExecutor{
		db:          db,
		cosProvider: cosProvider,
		taskService: taskService,
	}
}

// GetTaskType 返回执行器支持的任务类型
func (e *PackageUploadExecutor) GetTaskType() string {
	return asyncTaskModel.TaskTypeUploadPackageToCOS
}

// ValidateRequest 验证任务请求
func (e *PackageUploadExecutor) ValidateRequest(taskData string) error {
	var req PackageUploadRequest
	if err := json.Unmarshal([]byte(taskData), &req); err != nil {
		return fmt.Errorf("invalid task data format: %w", err)
	}

	if req.PackageID <= 0 {
		return fmt.Errorf("package_id is required")
	}
	if req.CloudAccountID == "" {
		return fmt.Errorf("cloud_account_id is required")
	}
	if req.COSBucket == "" {
		return fmt.Errorf("cos_bucket is required")
	}
	if req.COSPath == "" {
		return fmt.Errorf("cos_path is required")
	}
	if req.LocalFilePath == "" {
		return fmt.Errorf("local_file_path is required")
	}

	return nil
}

// Execute 执行任务
func (e *PackageUploadExecutor) Execute(ctx context.Context, task *asyncTaskModel.AsyncTask) error {
	log.InfoContextf(ctx, "[PackageUploadExecutor] 开始执行代码包上传任务, taskID: %s", task.TaskID)

	// 解析任务参数
	var req PackageUploadRequest
	if err := json.Unmarshal([]byte(task.RequestParams), &req); err != nil {
		return e.failTask(ctx, task.TaskID, fmt.Sprintf("解析任务参数失败: %v", err))
	}

	// 验证包是否存在
	_, err := e.getPackage(ctx, req.PackageID)
	if err != nil {
		return e.failTask(ctx, task.TaskID, fmt.Sprintf("获取包信息失败: %v", err))
	}

	// 获取云账户配置
	account, err := e.getCloudAccount(ctx, req.CloudAccountID)
	if err != nil {
		return e.failTask(ctx, task.TaskID, fmt.Sprintf("获取云账户配置失败: %v", err))
	}

	// 读取本地文件
	fileContent, err := e.readLocalFile(req.LocalFilePath)
	if err != nil {
		return e.failTask(ctx, task.TaskID, fmt.Sprintf("读取本地文件失败: %v", err))
	}

	// 上传到COS
	cosURL, err := e.uploadToCOS(ctx, account, req.COSBucket, req.COSPath, fileContent)
	if err != nil {
		return e.failTask(ctx, task.TaskID, fmt.Sprintf("上传到COS失败: %v", err))
	}

	// 更新包状态和URL
	if err := e.updatePackageStatus(ctx, req.PackageID, packageModel.PackageStatusAvailable, cosURL); err != nil {
		log.ErrorContextf(ctx, "[PackageUploadExecutor] 更新包状态失败: %v", err)
		// 这里不返回错误，因为上传已经成功了
	}

	// 标记任务完成
	return e.completeTask(ctx, task.TaskID, map[string]interface{}{
		"package_id": req.PackageID,
		"cos_url":    cosURL,
	})
}

// getPackage 获取包信息
func (e *PackageUploadExecutor) getPackage(ctx context.Context, packageID int64) (*packageModel.FunctionPackage, error) {
	var pkg packageModel.FunctionPackage
	err := e.db.Where("c_id = ? AND c_invalid = 0", packageID).First(&pkg).Error
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// getCloudAccount 获取云账户配置
func (e *PackageUploadExecutor) getCloudAccount(ctx context.Context, accountID string) (*cloudAccountModel.CloudAccount, error) {
	var account cloudAccountModel.CloudAccount
	err := e.db.Where("c_account_id = ? AND c_invalid = 0", accountID).First(&account).Error
	if err != nil {
		return nil, err
	}
	return &account, nil
}

// readLocalFile 读取本地文件
func (e *PackageUploadExecutor) readLocalFile(filePath string) ([]byte, error) {
	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}

	// 读取文件内容
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	return content, nil
}

// uploadToCOS 上传到COS
func (e *PackageUploadExecutor) uploadToCOS(ctx context.Context, account *cloudAccountModel.CloudAccount, bucket, path string, content []byte) (string, error) {
	// 根据云账户创建新的provider实例
	cosProvider, err := e.createCOSProvider(account)
	if err != nil {
		return "", fmt.Errorf("创建COS provider失败: %w", err)
	}

	uploadReq := &provider.UploadCOSRequest{
		Bucket:      bucket,
		Key:         path,
		Content:     content,
		ContentType: "application/zip",
	}

	resp, err := cosProvider.UploadCOS(ctx, uploadReq)
	if err != nil {
		return "", err
	}

	return resp.Location, nil
}

// createCOSProvider 根据云账户创建COS provider
func (e *PackageUploadExecutor) createCOSProvider(account *cloudAccountModel.CloudAccount) (provider.COS, error) {
	// 验证COS配置
	if account.COSBucket == "" {
		return nil, fmt.Errorf("COS bucket配置为空")
	}
	if account.AppID == "" {
		return nil, fmt.Errorf("AppID配置为空")
	}
	if account.COSRegion == "" {
		return nil, fmt.Errorf("COS region配置为空")
	}

	// 根据account的provider字段确定provider类型
	var providerType provider.ProviderType
	switch account.Provider {
	case "tencent":
		providerType = provider.ProviderTencent
	default:
		return nil, fmt.Errorf("unsupported provider: %s", account.Provider)
	}

	// 构建包含COS配置的ExtraConfig
	extraConfig := fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_app_id":"%s"}`, 
		account.COSRegion, account.COSBucket, account.AppID)

	// 创建云厂商配置
	config, err := provider.NewCloudConfig(providerType, account.SecretID, account.SecretKey, extraConfig)
	if err != nil {
		return nil, fmt.Errorf("创建云厂商配置失败: %w", err)
	}

	// 创建支持COS的provider实例
	cloudProvider, err := provider.NewTencentCloudProviderWithCOS(config)
	if err != nil {
		return nil, fmt.Errorf("创建COS provider失败: %w", err)
	}

	return cloudProvider, nil
}

// updatePackageStatus 更新包状态
func (e *PackageUploadExecutor) updatePackageStatus(ctx context.Context, packageID int64, status int, cosURL string) error {
	updates := map[string]interface{}{
		"c_status":          status,
		"c_upload_progress": 100,
		"c_mtime":           time.Now(),
	}

	if cosURL != "" {
		updates["c_cos_url"] = cosURL
		updates["c_cos_bucket"] = e.extractBucketFromURL(cosURL)
	}

	return e.db.Model(&packageModel.FunctionPackage{}).
		Where("c_id = ?", packageID).
		Updates(updates).Error
}

// extractBucketFromURL 从COS URL中提取bucket名称
func (e *PackageUploadExecutor) extractBucketFromURL(url string) string {
	// 简化实现，实际需要根据COS URL格式解析
	return ""
}

// failTask 标记任务失败
func (e *PackageUploadExecutor) failTask(ctx context.Context, taskID, errorMsg string) error {
	log.ErrorContextf(ctx, "[PackageUploadExecutor] 任务失败, taskID: %s, error: %s", taskID, errorMsg)

	return e.taskService.CompleteTask(ctx, taskID, asyncTaskModel.TaskStatusFailed, nil, errorMsg)
}

// completeTask 完成任务
func (e *PackageUploadExecutor) completeTask(ctx context.Context, taskID string, resultData interface{}) error {
	log.InfoContextf(ctx, "[PackageUploadExecutor] 任务完成, taskID: %s", taskID)

	return e.taskService.CompleteTask(ctx, taskID, asyncTaskModel.TaskStatusSuccess, resultData, "")
}
