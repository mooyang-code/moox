package cloudnode

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/provider"

	"trpc.group/trpc-go/trpc-go/log"
)

// UploadPackageExecutor 代码包上传到COS执行器
type UploadPackageExecutor struct {
	packageDAO dao.FunctionPackageDAO
	accountDAO dao.CloudAccountDAO
}

// NewUploadPackageExecutor 创建代码包上传执行器
func NewUploadPackageExecutor(packageDAO dao.FunctionPackageDAO, accountDAO dao.CloudAccountDAO) *UploadPackageExecutor {
	return &UploadPackageExecutor{
		packageDAO: packageDAO,
		accountDAO: accountDAO,
	}
}

// Name 返回执行器外显名称
func (e *UploadPackageExecutor) Name() string {
	return "代码包上传到COS"
}

// Type 返回执行器类型
func (e *UploadPackageExecutor) Type() string {
	return asynctask.TaskTypeUploadFileToCOS
}

// UploadPackageExecutorRequest 代码包上传执行器请求
type UploadPackageExecutorRequest struct {
	PackageName    string `json:"package_name"`
	Version        string `json:"version"`
	Description    string `json:"description"`
	Runtime        string `json:"runtime"`
	PackageType    string `json:"package_type"`
	BizType        string `json:"biz_type"`
	CloudAccountID string `json:"cloud_account_id"`
	FilePath       string `json:"file_path"`       // 本地文件路径（新增）
	FileContent    string `json:"file_content"`    // base64编码的文件内容（保留用于向后兼容，优先使用FilePath）
}

// UploadPackageExecutorResponse 代码包上传执行器响应
type UploadPackageExecutorResponse struct {
	PackageID   string `json:"package_id"`
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Status      int    `json:"status"`
	FileSize    int64  `json:"file_size"`
	FileMD5     string `json:"file_md5"`
	COSURL      string `json:"cos_url"`
}

// Execute 执行代码包上传任务
func (e *UploadPackageExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
	// 1. 解析和预处理
	req, fileContent, err := e.parseAndPreprocess(ctx, taskID, requestParams)
	if err != nil {
		return "", err
	}

	// 如果使用了临时文件，在任务完成后清理
	if req.FilePath != "" {
		defer func() {
			if err := os.Remove(req.FilePath); err != nil {
				log.WarnContextf(ctx, "[UploadFileExecutor] Failed to remove temp file %s: %v", req.FilePath, err)
			} else {
				log.InfoContextf(ctx, "[UploadFileExecutor] Removed temp file: %s", req.FilePath)
			}
		}()
	}

	// 2. 准备包数据
	packageData, cosPath, err := e.preparePackageData(req, fileContent)
	if err != nil {
		return "", err
	}
	packageID := packageData.PackageID

	// 3. 保存本地文件和数据库记录
	if err := e.savePackageRecord(ctx, packageData, fileContent, cosPath); err != nil {
		return "", err
	}

	// 4. 上传到COS
	uploadResp, err := e.uploadToCOS(ctx, packageID, req.CloudAccountID, fileContent, cosPath)
	if err != nil {
		return "", err
	}

	// 5. 更新数据库状态
	if err := e.updatePackageStatus(ctx, packageID, req.CloudAccountID, uploadResp.Location, cosPath); err != nil {
		return "", err
	}

	// 6. 构建并返回结果
	fileSize := int64(len(fileContent))
	result := UploadPackageExecutorResponse{
		PackageID:   packageID,
		PackageName: req.PackageName,
		Version:     req.Version,
		Status:      model.PackageStatusAvailable,
		FileSize:    fileSize,
		FileMD5:     packageData.FileMD5,
		COSURL:      uploadResp.Location,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}
	return string(resultJSON), nil
}

// parseAndPreprocess 解析请求参数并预处理文件内容
func (e *UploadPackageExecutor) parseAndPreprocess(ctx context.Context, taskID, requestParams string) (*UploadPackageExecutorRequest, []byte, error) {
	// 解析请求参数
	var req UploadPackageExecutorRequest
	if err := json.Unmarshal([]byte(requestParams), &req); err != nil {
		return nil, nil, fmt.Errorf("failed to parse request params: %w", err)
	}
	log.InfoContextf(ctx, "[UploadFileExecutor] Starting upload: TaskID=%s, PackageName=%s, Version=%s",
		taskID, req.PackageName, req.Version)

	// 验证必填参数
	if req.CloudAccountID == "" {
		return nil, nil, fmt.Errorf("cloud_account_id is required")
	}
	if req.PackageName == "" {
		return nil, nil, fmt.Errorf("package_name is required")
	}
	if req.Version == "" {
		return nil, nil, fmt.Errorf("version is required")
	}

	var fileContent []byte
	var err error

	// 优先从本地文件路径读取
	if req.FilePath != "" {
		log.InfoContextf(ctx, "[UploadFileExecutor] Reading file from local path: %s", req.FilePath)
		fileContent, err = os.ReadFile(req.FilePath)
		if err != nil {
			return nil, nil, fmt.Errorf("读取本地文件失败: %w", err)
		}
		log.InfoContextf(ctx, "[UploadFileExecutor] Read file from path, size: %d bytes", len(fileContent))
	} else if req.FileContent != "" {
		// 向后兼容：如果没有FilePath，尝试从FileContent解码
		log.InfoContextf(ctx, "[UploadFileExecutor] Using base64 file content (legacy mode)")
		fileContent, err = base64.StdEncoding.DecodeString(req.FileContent)
		if err != nil {
			return nil, nil, fmt.Errorf("解码base64文件内容失败: %w", err)
		}
	} else {
		return nil, nil, fmt.Errorf("file_path or file_content is required")
	}

	return &req, fileContent, nil
}

// preparePackageData 准备包数据和路径
func (e *UploadPackageExecutor) preparePackageData(req *UploadPackageExecutorRequest, fileContent []byte) (*model.FunctionPackage, string, error) {
	// 计算文件MD5和大小
	fileMD5 := e.calculateMD5(fileContent)
	fileSize := int64(len(fileContent))

	// 生成包ID
	packageID := model.GeneratePackageID()

	// 生成COS路径
	cosPath := e.generateCOSPath(req.PackageType, req.PackageName, req.Version, packageID)

	// 从COS路径提取文件名
	filename := filepath.Base(cosPath)

	// 创建代码包记录
	packageData := &model.FunctionPackage{
		PackageID:        packageID,
		PackageName:      req.PackageName,
		Version:          req.Version,
		Description:      req.Description,
		Runtime:          req.Runtime,
		PackageType:      req.PackageType,
		BizType:          req.BizType,
		OriginalFilename: filename,
		FileSize:         fileSize,
		FileMD5:          fileMD5,
		CloudAccountID:   req.CloudAccountID,
		Status:           model.PackageStatusUploading,
	}
	return packageData, cosPath, nil
}

// savePackageRecord 保存本地文件和数据库记录
func (e *UploadPackageExecutor) savePackageRecord(ctx context.Context, packageData *model.FunctionPackage, fileContent []byte, cosPath string) error {
	// 保存到本地文件
	if _, err := e.saveToLocalFile(fileContent, cosPath); err != nil {
		return fmt.Errorf("保存本地文件失败: %w", err)
	}

	// 保存到数据库
	if err := e.packageDAO.Create(ctx, packageData); err != nil {
		return fmt.Errorf("创建代码包记录失败: %w", err)
	}

	log.InfoContextf(ctx, "[UploadFileExecutor] Created package record: PackageID=%s", packageData.PackageID)
	return nil
}

// uploadToCOS 上传文件到COS
func (e *UploadPackageExecutor) uploadToCOS(ctx context.Context, packageID,
	cloudAccountID string, fileContent []byte, cosPath string) (*provider.UploadCOSResponse, error) {
	// 获取 COS 账户信息（直接使用 accountDAO）
	account, err := e.accountDAO.GetCloudAccount(ctx, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("获取云账户信息失败: %w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("云账户不存在: account_id=%s", cloudAccountID)
	}
	accountInfo := &COSAccountInfo{
		Provider:  account.Provider,
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		AppID:     account.AppID,
		COSRegion: account.COSRegion,
		COSBucket: account.COSBucket,
	}

	// 创建COS客户端
	cosClient, err := e.createCOSClient(ctx, accountInfo)
	if err != nil {
		return nil, fmt.Errorf("创建COS客户端失败: %w", err)
	}

	// 上传到COS
	log.InfoContextf(ctx, "[UploadFileExecutor] Uploading to COS: bucket=%s, path=%s", accountInfo.COSBucket, cosPath)

	uploadReq := &provider.UploadCOSRequest{
		Bucket:      accountInfo.COSBucket,
		Key:         cosPath,
		Content:     fileContent,
		ContentType: "application/zip",
	}

	uploadResp, err := cosClient.UploadCOS(ctx, uploadReq)
	if err != nil {
		// 上传失败，更新状态为失败
		_ = e.packageDAO.Update(ctx, packageID, map[string]interface{}{
			"c_status":        model.PackageStatusFailed,
			"c_error_message": fmt.Sprintf("上传到COS失败: %v", err),
		})
		return nil, fmt.Errorf("上传到COS失败: %w", err)
	}
	return uploadResp, nil
}

// updatePackageStatus 更新包的状态信息到数据库
func (e *UploadPackageExecutor) updatePackageStatus(ctx context.Context, packageID, cloudAccountID string, cosURL, cosPath string) error {
	// 获取 COS 账户信息
	account, err := e.accountDAO.GetCloudAccount(ctx, cloudAccountID)
	if err != nil {
		return fmt.Errorf("获取云账户信息失败: %w", err)
	}
	// 更新数据库中的COS信息
	updates := map[string]interface{}{
		"c_cos_bucket":      account.COSBucket,
		"c_cos_path":        cosPath,
		"c_cos_url":         cosURL,
		"c_cos_region":      account.COSRegion,
		"c_status":          model.PackageStatusAvailable,
		"c_upload_progress": 100,
	}
	if err := e.packageDAO.Update(ctx, packageID, updates); err != nil {
		log.ErrorContextf(ctx, "[UploadFileExecutor] Failed to update package info: %v", err)
		// 上传成功但更新数据库失败，记录日志但不返回错误
	}

	log.InfoContextf(ctx, "[UploadFileExecutor] Upload successful: PackageID=%s, COSURL=%s",
		packageID, cosURL)
	return nil
}

// createCOSClient 创建COS客户端
func (e *UploadPackageExecutor) createCOSClient(ctx context.Context, account *COSAccountInfo) (provider.Client, error) {
	log.InfoContextf(ctx, "[UploadFileExecutor] Creating COS client: Provider=%s, Region=%s, Bucket=%s",
		account.Provider, account.COSRegion, account.COSBucket)

	// 解析云平台类型
	platformType, err := provider.ParseCloudPlatform(account.Provider)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadFileExecutor] Failed to parse cloud platform: %v", err)
		return nil, fmt.Errorf("不支持的云平台类型: %w", err)
	}
	log.InfoContextf(ctx, "[UploadFileExecutor] Parsed cloud platform type: %s", platformType)

	// 构建配置
	extraConfig := fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_app_id":"%s"}`,
		account.COSRegion, account.COSBucket, account.AppID)

	// 创建云平台配置
	config, err := provider.NewConfig(platformType, account.SecretID, account.SecretKey, extraConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadFileExecutor] Failed to create cloud config: %v", err)
		return nil, fmt.Errorf("创建云配置失败: %w", err)
	}
	log.InfoContextf(ctx, "[UploadFileExecutor] Cloud config created successfully")

	// 使用工厂方法创建支持COS的云厂商客户端
	cosProvider, err := provider.NewClient(config)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadFileExecutor] Failed to create COS provider: %v", err)
		return nil, fmt.Errorf("创建COS客户端失败: %w", err)
	}
	log.InfoContextf(ctx, "[UploadFileExecutor] COS client created successfully")
	return cosProvider, nil
}

// generateCOSPath 生成COS文件路径
func (e *UploadPackageExecutor) generateCOSPath(packageType, packageName, version, packageID string) string {
	timestamp := time.Now().Unix()
	filename := fmt.Sprintf("%s-%s-%d-%s.zip", packageName, version, timestamp, packageID)

	// 数据采集器类型：直接在data_collector下按版本存储
	if packageType == model.PackageTypeDataCollector {
		return fmt.Sprintf("%s/%s/%s", packageType, version, filename)
	}

	// 因子计算器类型：在factor_calculator下按具体因子名称和版本存储
	return fmt.Sprintf("%s/%s/%s/%s", packageType, packageName, version, filename)
}

// saveToLocalFile 保存文件到本地存储目录（使用与COS路径一致的路径结构）
func (e *UploadPackageExecutor) saveToLocalFile(content []byte, cosPath string) (string, error) {
	// 使用COS路径作为本地存储的相对路径
	filePath := constants.GetPackageStorageFilePath(cosPath)

	// 确保文件的父目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	return filePath, nil
}

// calculateMD5 计算文件MD5
func (e *UploadPackageExecutor) calculateMD5(content []byte) string {
	hash := md5.Sum(content)
	return hex.EncodeToString(hash[:])
}
