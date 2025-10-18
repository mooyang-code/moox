package logic

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	asyncTaskModel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-go/log"
)

// validateUploadRequest 验证上传请求参数
func (s *FunctionPackageService) validateUploadRequest(req *UploadPackageRequest) error {
	// 验证函数包类型
	if req.PackageType != model.PackageTypeDataCollector && req.PackageType != model.PackageTypeFactorCalculator {
		return fmt.Errorf("无效的函数包类型: %s", req.PackageType)
	}

	// 验证数据采集器命名规则
	if req.PackageType == model.PackageTypeDataCollector && req.PackageName != "data_collector" {
		return fmt.Errorf("数据采集器的包名必须为 'data_collector'")
	}

	// 验证运行时环境
	validRuntimes := []string{
		model.RuntimeGo1,
		model.RuntimePython37,
		model.RuntimePython39,
		model.RuntimeNodejs14,
		model.RuntimeNodejs16,
	}

	valid := false
	for _, runtime := range validRuntimes {
		if req.Runtime == runtime {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("无效的运行时环境: %s", req.Runtime)
	}

	// 验证版本号格式（简单的v开头检查）
	if !strings.HasPrefix(req.Version, "v") {
		return fmt.Errorf("版本号必须以'v'开头，如: v1.0.0")
	}

	// 验证文件内容不为空
	if req.FileContent == "" {
		return fmt.Errorf("文件内容不能为空")
	}

	return nil
}

// validateAndPreprocess 验证和预处理上传请求
func (s *FunctionPackageService) validateAndPreprocess(ctx context.Context, req *UploadPackageRequest) ([]byte, string, error) {
	log.InfoContextf(ctx, "[validateAndPreprocess] 开始验证参数")

	// 验证输入参数
	if err := s.validateUploadRequest(req); err != nil {
		return nil, "", fmt.Errorf("参数验证失败: %w", err)
	}

	// 检查版本是否已存在
	exists, err := s.checkVersionExists(ctx, req.PackageName, req.Version)
	if err != nil {
		return nil, "", fmt.Errorf("检查版本失败: %w", err)
	}
	if exists {
		return nil, "", NewVersionExistsError(req.PackageName, req.Version)
	}

	// 解码base64文件内容
	fileContent, err := base64.StdEncoding.DecodeString(req.FileContent)
	if err != nil {
		return nil, "", fmt.Errorf("文件内容解码失败: %w", err)
	}

	// 计算文件MD5
	fileMD5 := s.calculateMD5(fileContent)

	log.InfoContextf(ctx, "[validateAndPreprocess] 验证完成, 文件大小: %d bytes, MD5: %s", len(fileContent), fileMD5)
	return fileContent, fileMD5, nil
}

// createPackageRecord 创建数据库记录
func (s *FunctionPackageService) createPackageRecord(ctx context.Context,
	req *UploadPackageRequest, fileContent []byte, fileMD5 string, storageConfig *StorageConfig) (*model.FunctionPackage, error) {
	log.InfoContextf(ctx, "[createPackageRecord] 开始创建数据库记录")

	pkg := &model.FunctionPackage{
		PackageName:      req.PackageName,
		Version:          req.Version,
		Description:      req.Description,
		Runtime:          req.Runtime,
		PackageType:      req.PackageType,
		OriginalFilename: fmt.Sprintf("%s-%s.zip", req.PackageName, req.Version),
		FileSize:         int64(len(fileContent)),
		FileMD5:          fileMD5,
		COSBucket:        storageConfig.Bucket,
		COSPath:          storageConfig.Path,
		Status:           model.PackageStatusUploading,
		UploadProgress:   0,
		CreatedBy:        req.CreatedBy,
		Invalid:          0,
		CTime:            time.Now(),
		MTime:            time.Now(),
	}

	if err := s.dao.Create(ctx, pkg); err != nil {
		return nil, fmt.Errorf("创建数据库记录失败: %w", err)
	}

	log.InfoContextf(ctx, "[createPackageRecord] 数据库记录创建成功, ID: %d", pkg.ID)
	return pkg, nil
}

// finalizeUpload 完成上传并更新状态
func (s *FunctionPackageService) finalizeUpload(ctx context.Context, pkg *model.FunctionPackage, cosURL string) error {
	log.InfoContextf(ctx, "[finalizeUpload] 开始完成上传, ID: %d", pkg.ID)

	// 更新数据库状态
	err := s.updatePackageStatus(ctx, pkg.ID, model.PackageStatusAvailable, 100, "")
	if err != nil {
		return fmt.Errorf("更新状态失败: %w", err)
	}

	// 更新访问URL
	if cosURL != "" {
		s.dao.Update(ctx, pkg.ID, map[string]interface{}{"c_cos_url": cosURL})
	}

	log.InfoContextf(ctx, "[finalizeUpload] 上传完成, ID: %d, URL: %s", pkg.ID, cosURL)
	return nil
}

// UploadPackageAsync 异步上传代码包
func (s *FunctionPackageService) UploadPackageAsync(ctx context.Context, req *UploadPackageRequest) (*UploadPackageAsyncResponse, error) {
	log.InfoContextf(ctx, "[UploadPackageAsync] 开始异步上传代码包, 包名: %s, 版本: %s, 类型: %s",
		req.PackageName, req.Version, req.PackageType)

	// 1. 验证和预处理
	fileContent, fileMD5, err := s.validateAndPreprocess(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 验证和预处理失败: %v", err)
		return nil, err
	}

	// 2. 确定存储策略
	storageConfig, err := s.determineStorageStrategy(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 确定存储策略失败: %v", err)
		return nil, err
	}

	// 3. 保存文件到本地临时目录
	localPath, err := s.saveToLocalFile(fileContent, storageConfig.Path)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 保存本地文件失败: %v", err)
		return nil, err
	}

	// 4. 创建数据库记录（初始状态为上传中）
	pkg, err := s.createPackageRecord(ctx, req, fileContent, fileMD5, storageConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 创建数据库记录失败: %v", err)
		return nil, err
	}

	// 5. 如果是本地存储，直接完成上传
	if !storageConfig.UseCOS {
		err = s.finalizeUpload(ctx, pkg, localPath)
		if err != nil {
			log.ErrorContextf(ctx, "[UploadPackageAsync] 完成本地上传失败: %v", err)
			return nil, err
		}

		return &UploadPackageAsyncResponse{
			TaskID:      "", // 本地存储不需要任务ID
			PackageID:   pkg.ID,
			PackageName: pkg.PackageName,
			Version:     pkg.Version,
			Status:      model.PackageStatusAvailable,
			IsAsync:     false,
		}, nil
	}

	// 6. 对于COS存储，创建异步任务
	if s.taskService == nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 异步任务服务未配置")
		return nil, fmt.Errorf("异步任务服务未配置")
	}

	// 生成任务ID
	taskID := uuid.New().String()

	// 创建异步任务请求
	taskReq := map[string]interface{}{
		"package_id":       pkg.ID,
		"cloud_account_id": req.CloudAccountID,
		"cos_bucket":       storageConfig.Bucket,
		"cos_path":         storageConfig.Path,
		"local_file_path":  localPath,
	}

	// 创建并执行异步任务
	err = s.taskService.CreateAndExecuteTask(ctx, taskID, asyncTaskModel.TaskTypeUploadPackageToCOS, 1, taskReq)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackageAsync] 创建异步任务失败: %v", err)
		return nil, fmt.Errorf("创建异步任务失败: %w", err)
	}

	log.InfoContextf(ctx, "[UploadPackageAsync] 异步上传任务创建成功, 包名: %s, 版本: %s, 任务ID: %s", req.PackageName, req.Version, taskID)
	return &UploadPackageAsyncResponse{
		TaskID:      taskID,
		PackageID:   pkg.ID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Status:      model.PackageStatusUploading,
		IsAsync:     true,
	}, nil
}

// GetUploadTaskStatus 获取上传任务状态
func (s *FunctionPackageService) GetUploadTaskStatus(ctx context.Context, taskID string) (*UploadTaskStatusResponse, error) {
	if s.taskService == nil {
		return nil, fmt.Errorf("异步任务服务未配置")
	}

	task, err := s.taskService.GetTaskStatus(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("获取任务状态失败: %w", err)
	}

	// 根据任务状态映射到字符串状态
	var status string
	var message string

	switch task.TaskStatus {
	case asyncTaskModel.TaskStatusPending:
		status = "pending"
		message = "任务等待处理"
	case asyncTaskModel.TaskStatusProcessing:
		status = "processing"
		message = "正在上传到COS"
	case asyncTaskModel.TaskStatusSuccess:
		status = "success"
		message = "上传成功"
	case asyncTaskModel.TaskStatusFailed:
		status = "failed"
		message = task.ErrorMessage
		if message == "" {
			message = "上传失败"
		}
	case asyncTaskModel.TaskStatusPartial:
		status = "partial"
		message = "部分成功"
	case asyncTaskModel.TaskStatusCancelled:
		status = "cancelled"
		message = "任务已取消"
	default:
		status = "unknown"
		message = "未知状态"
	}

	return &UploadTaskStatusResponse{
		TaskID:      taskID,
		PackageID:   0,
		PackageName: "",
		Version:     "",
		Status:      status,
		Progress:    task.GetProgress(),
		Message:     message,
		CreatedAt:   task.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   task.UpdatedAt.Format(time.RFC3339),
	}, nil
}
