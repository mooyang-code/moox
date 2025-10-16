package logic

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	cloudAccountModel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// FunctionPackageService 云函数代码包服务
type FunctionPackageService struct {
	db          *gorm.DB
	cosProvider provider.CloudProviderWithCOS
	cosBucket   string
}

// NewFunctionPackageService 创建云函数代码包服务
func NewFunctionPackageService(db *gorm.DB, cosProvider provider.CloudProviderWithCOS, cosBucket string) *FunctionPackageService {
	return &FunctionPackageService{
		db:          db,
		cosProvider: cosProvider,
		cosBucket:   cosBucket,
	}
}

// getCOSConfigFromAccount 从云账户获取COS配置
func (s *FunctionPackageService) getCOSConfigFromAccount(ctx context.Context, accountID string) (*cloudAccountModel.CloudAccount, bool, error) {
	var account cloudAccountModel.CloudAccount
	err := s.db.Where("c_account_id = ? AND c_invalid = 0", accountID).First(&account).Error
	if err != nil {
		return nil, false, fmt.Errorf("云账户不存在: %w", err)
	}

	// 检查COS配置是否完整
	hasCOSConfig := account.COSRegion != "" && account.COSBucket != ""
	return &account, hasCOSConfig, nil
}

// saveToLocalFile 保存文件到本地/tmp目录
func (s *FunctionPackageService) saveToLocalFile(content []byte, filename string) (string, error) {
	tmpDir := "/tmp/moox/packages"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}

	filePath := filepath.Join(tmpDir, filename)
	if err := ioutil.WriteFile(filePath, content, 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	return filePath, nil
}

// UploadPackageRequest 上传代码包请求
type UploadPackageRequest struct {
	PackageName    string `json:"package_name" binding:"required"`
	Version        string `json:"version" binding:"required"`
	Description    string `json:"description"`
	Runtime        string `json:"runtime" binding:"required"`
	PackageType    string `json:"package_type" binding:"required"`
	FileContent    string `json:"file_content" binding:"required"` // base64编码的zip文件内容
	CloudAccountID string `json:"cloud_account_id"`                // 云账户ID，可选，用于COS配置
	CreatedBy      string `json:"-"`                               // 从JWT中获取
}

// UploadPackageResponse 上传代码包响应
type UploadPackageResponse struct {
	ID          int64  `json:"id"`
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Status      int    `json:"status"`
	COSURL      string `json:"cos_url"`
}

// StorageConfig 存储配置
type StorageConfig struct {
	UseCOS bool
	Bucket string
	Path   string
}

// generateCOSPath 生成COS文件路径
func (s *FunctionPackageService) generateCOSPath(packageType, packageName, version string) string {
	timestamp := time.Now().Unix()
	filename := fmt.Sprintf("%s-%s-%d.zip", packageName, version, timestamp)

	// 数据采集器类型：直接在data_collector下按版本存储
	if packageType == model.PackageTypeDataCollector {
		return fmt.Sprintf("packages/%s/%s/%s", packageType, version, filename)
	}

	// 因子计算器类型：在factor_calculator下按具体因子名称和版本存储
	return fmt.Sprintf("packages/%s/%s/%s/%s", packageType, packageName, version, filename)
}

// calculateMD5 计算内容的MD5值
func (s *FunctionPackageService) calculateMD5(content []byte) string {
	hash := md5.Sum(content)
	return hex.EncodeToString(hash[:])
}

// UploadPackage 上传代码包
func (s *FunctionPackageService) UploadPackage(ctx context.Context, req *UploadPackageRequest) (*UploadPackageResponse, error) {
	log.InfoContextf(ctx, "[UploadPackage] 开始上传代码包, 包名: %s, 版本: %s, 类型: %s", req.PackageName, req.Version, req.PackageType)

	// 1. 验证和预处理
	fileContent, fileMD5, err := s.validateAndPreprocess(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackage] 验证和预处理失败: %v", err)
		return nil, err
	}

	// 2. 确定存储策略
	storageConfig, err := s.determineStorageStrategy(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackage] 确定存储策略失败: %v", err)
		return nil, err
	}
	log.InfoContextf(ctx, "[UploadPackage] 存储策略: useCOS=%t, bucket=%s", storageConfig.UseCOS, storageConfig.Bucket)

	// 3. 创建数据库记录
	pkg, err := s.createPackageRecord(ctx, req, fileContent, fileMD5, storageConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackage] 创建数据库记录失败: %v", err)
		return nil, err
	}
	log.InfoContextf(ctx, "[UploadPackage] 数据库记录创建成功, ID: %d", pkg.ID)

	// 4. 执行文件存储
	cosURL, err := s.executeFileStorage(ctx, pkg, fileContent, storageConfig)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackage] 文件存储失败: %v", err)
		return nil, err
	}

	// 5. 完成上传并更新状态
	err = s.finalizeUpload(ctx, pkg, cosURL)
	if err != nil {
		log.ErrorContextf(ctx, "[UploadPackage] 完成上传失败: %v", err)
		return nil, err
	}

	log.InfoContextf(ctx, "[UploadPackage] 上传完成, 包名: %s, 版本: %s, URL: %s", req.PackageName, req.Version, cosURL)
	return &UploadPackageResponse{
		ID:          pkg.ID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Status:      model.PackageStatusAvailable,
		COSURL:      cosURL,
	}, nil
}

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

// checkVersionExists 检查版本是否已存在
func (s *FunctionPackageService) checkVersionExists(packageName, version string) (bool, error) {
	var count int64
	err := s.db.Model(&model.FunctionPackage{}).
		Where("c_package_name = ? AND c_version = ? AND c_invalid = 0", packageName, version).
		Count(&count).Error

	return count > 0, err
}

// updatePackageStatus 更新代码包状态
func (s *FunctionPackageService) updatePackageStatus(id int64, status, progress int, errorMsg string) error {
	updates := map[string]interface{}{
		"c_status":          status,
		"c_upload_progress": progress,
		"c_mtime":           time.Now(),
	}

	if errorMsg != "" {
		updates["c_error_message"] = errorMsg
	}

	return s.db.Model(&model.FunctionPackage{}).
		Where("c_id = ?", id).
		Updates(updates).Error
}

// PackageListRequest 代码包列表请求
type PackageListRequest struct {
	Page        int    `form:"page,default=1"`
	PageSize    int    `form:"page_size,default=20"`
	PackageName string `form:"package_name"`
	Runtime     string `form:"runtime"`
	PackageType string `form:"package_type"`
	Status      *int   `form:"status"`
}

// PackageListResponse 代码包列表响应
type PackageListResponse struct {
	Total int64                `json:"total"`
	Items []*PackageListItemVO `json:"items"`
}

// PackageListItemVO 代码包列表项视图对象
type PackageListItemVO struct {
	ID               int64      `json:"id"`
	PackageName      string     `json:"package_name"`
	Version          string     `json:"version"`
	Description      string     `json:"description"`
	Runtime          string     `json:"runtime"`
	PackageType      string     `json:"package_type"`
	PackageTypeLabel string     `json:"package_type_label"`
	FileSize         int64      `json:"file_size"`
	Status           int        `json:"status"`
	StatusLabel      string     `json:"status_label"`
	LastDeployTime   *time.Time `json:"last_deploy_time"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
}

// GetPackageList 获取代码包列表
func (s *FunctionPackageService) GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error) {
	// 构建查询条件
	query := s.db.Model(&model.FunctionPackage{}).Where("c_invalid = 0")

	if req.PackageName != "" {
		query = query.Where("c_package_name LIKE ?", "%"+req.PackageName+"%")
	}
	if req.Runtime != "" {
		query = query.Where("c_runtime = ?", req.Runtime)
	}
	if req.PackageType != "" {
		query = query.Where("c_package_type = ?", req.PackageType)
	}
	if req.Status != nil {
		query = query.Where("c_status = ?", *req.Status)
	}

	// 查询总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("查询总数失败: %w", err)
	}

	// 查询列表数据
	var packages []*model.FunctionPackage
	offset := (req.Page - 1) * req.PageSize
	if err := query.Order("c_ctime DESC").Offset(offset).Limit(req.PageSize).Find(&packages).Error; err != nil {
		return nil, fmt.Errorf("查询列表失败: %w", err)
	}

	// 转换为VO
	items := make([]*PackageListItemVO, len(packages))
	for i, pkg := range packages {
		items[i] = &PackageListItemVO{
			ID:               pkg.ID,
			PackageName:      pkg.PackageName,
			Version:          pkg.Version,
			Description:      pkg.Description,
			Runtime:          pkg.Runtime,
			PackageType:      pkg.PackageType,
			PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),
			FileSize:         pkg.FileSize,
			Status:           pkg.Status,
			StatusLabel:      model.GetStatusDisplayName(pkg.Status),
			LastDeployTime:   pkg.LastDeployTime,
			CreatedBy:        pkg.CreatedBy,
			CreatedAt:        pkg.CTime,
		}
	}

	return &PackageListResponse{
		Total: total,
		Items: items,
	}, nil
}

// GetPackageDetail 获取代码包详情
func (s *FunctionPackageService) GetPackageDetail(ctx context.Context, id int64) (*model.FunctionPackage, error) {
	var pkg model.FunctionPackage
	err := s.db.Where("c_id = ? AND c_invalid = 0", id).First(&pkg).Error
	if err != nil {
		return nil, fmt.Errorf("查询代码包详情失败: %w", err)
	}
	return &pkg, nil
}

// DeletePackage 删除代码包（软删除）
func (s *FunctionPackageService) DeletePackage(ctx context.Context, id int64) error {
	return s.db.Model(&model.FunctionPackage{}).
		Where("c_id = ?", id).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_status":  model.PackageStatusDeleted,
			"c_mtime":   time.Now(),
		}).Error
}

// GetPackageDownloadURL 获取代码包下载URL
func (s *FunctionPackageService) GetPackageDownloadURL(ctx context.Context, id int64) (string, error) {
	pkg, err := s.GetPackageDetail(ctx, id)
	if err != nil {
		return "", err
	}

	if pkg.Status != model.PackageStatusAvailable {
		return "", fmt.Errorf("代码包状态不可用，当前状态: %s", model.GetStatusDisplayName(pkg.Status))
	}

	// 检查是否为本地存储
	if pkg.COSBucket == "local" {
		// 本地存储直接返回文件路径，前端需要通过API下载
		return fmt.Sprintf("/api/v1/function-packages/%d/download-local", pkg.ID), nil
	}

	// COS存储生成预签名URL
	url, err := s.cosProvider.GetCOSObjectURL(ctx, pkg.COSBucket, pkg.COSPath, 24*time.Hour)
	if err != nil {
		return "", fmt.Errorf("生成下载链接失败: %w", err)
	}

	return url, nil
}

// DownloadLocalPackage 下载本地存储的代码包
func (s *FunctionPackageService) DownloadLocalPackage(ctx context.Context, id int64) ([]byte, string, error) {
	pkg, err := s.GetPackageDetail(ctx, id)
	if err != nil {
		return nil, "", err
	}

	if pkg.Status != model.PackageStatusAvailable {
		return nil, "", fmt.Errorf("代码包状态不可用，当前状态: %s", model.GetStatusDisplayName(pkg.Status))
	}

	if pkg.COSBucket != "local" {
		return nil, "", fmt.Errorf("该代码包不是本地存储")
	}

	// 读取本地文件
	content, err := ioutil.ReadFile(pkg.COSPath)
	if err != nil {
		return nil, "", fmt.Errorf("读取本地文件失败: %w", err)
	}

	return content, pkg.OriginalFilename, nil
}

// validateAndPreprocess 验证和预处理上传请求
func (s *FunctionPackageService) validateAndPreprocess(ctx context.Context, req *UploadPackageRequest) ([]byte, string, error) {
	log.InfoContextf(ctx, "[validateAndPreprocess] 开始验证参数")
	
	// 验证输入参数
	if err := s.validateUploadRequest(req); err != nil {
		return nil, "", fmt.Errorf("参数验证失败: %w", err)
	}

	// 检查版本是否已存在
	exists, err := s.checkVersionExists(req.PackageName, req.Version)
	if err != nil {
		return nil, "", fmt.Errorf("检查版本失败: %w", err)
	}
	if exists {
		return nil, "", fmt.Errorf("代码包 %s 版本 %s 已存在", req.PackageName, req.Version)
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

// determineStorageStrategy 确定存储策略
func (s *FunctionPackageService) determineStorageStrategy(ctx context.Context, req *UploadPackageRequest) (*StorageConfig, error) {
	log.InfoContextf(ctx, "[determineStorageStrategy] 开始确定存储策略, 云账户ID: %s", req.CloudAccountID)
	
	config := &StorageConfig{}

	if req.CloudAccountID != "" {
		// 从云账户获取COS配置
		account, hasCOSConfig, err := s.getCOSConfigFromAccount(ctx, req.CloudAccountID)
		if err != nil {
			log.WarnContextf(ctx, "[determineStorageStrategy] 获取云账户配置失败: %v, 降级到本地存储", err)
		} else if hasCOSConfig {
			config.UseCOS = true
			config.Bucket = account.COSBucket
			config.Path = s.generateCOSPath(req.PackageType, req.PackageName, req.Version)
			log.InfoContextf(ctx, "[determineStorageStrategy] 使用COS存储, bucket: %s, path: %s", config.Bucket, config.Path)
			return config, nil
		}
	}

	// 降级到本地存储
	config.UseCOS = false
	config.Bucket = "local"
	config.Path = fmt.Sprintf("%s-%s-%d.zip", req.PackageName, req.Version, time.Now().Unix())
	log.InfoContextf(ctx, "[determineStorageStrategy] 使用本地存储, path: %s", config.Path)
	
	return config, nil
}

// createPackageRecord 创建数据库记录
func (s *FunctionPackageService) createPackageRecord(ctx context.Context, req *UploadPackageRequest, fileContent []byte, fileMD5 string, storageConfig *StorageConfig) (*model.FunctionPackage, error) {
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

	if err := s.db.Create(pkg).Error; err != nil {
		return nil, fmt.Errorf("创建数据库记录失败: %w", err)
	}

	log.InfoContextf(ctx, "[createPackageRecord] 数据库记录创建成功, ID: %d", pkg.ID)
	return pkg, nil
}

// executeFileStorage 执行文件存储
func (s *FunctionPackageService) executeFileStorage(ctx context.Context, pkg *model.FunctionPackage, fileContent []byte, storageConfig *StorageConfig) (string, error) {
	log.InfoContextf(ctx, "[executeFileStorage] 开始执行文件存储, useCOS: %t", storageConfig.UseCOS)
	
	if storageConfig.UseCOS {
		return s.uploadToCOS(ctx, pkg, fileContent, storageConfig)
	} else {
		return s.saveToLocal(ctx, pkg, fileContent, storageConfig)
	}
}

// uploadToCOS 上传到COS
func (s *FunctionPackageService) uploadToCOS(ctx context.Context, pkg *model.FunctionPackage, fileContent []byte, storageConfig *StorageConfig) (string, error) {
	log.InfoContextf(ctx, "[uploadToCOS] 开始上传到COS, bucket: %s, path: %s", storageConfig.Bucket, storageConfig.Path)
	
	uploadReq := &provider.UploadCOSRequest{
		Bucket:      storageConfig.Bucket,
		Key:         storageConfig.Path,
		Content:     fileContent,
		ContentType: "application/zip",
	}

	cosResp, err := s.cosProvider.UploadCOS(ctx, uploadReq)
	if err != nil {
		log.WarnContextf(ctx, "[uploadToCOS] COS上传失败: %v, 降级到本地存储", err)
		// COS上传失败，降级到本地存储
		localPath, localErr := s.saveToLocalFile(fileContent, storageConfig.Path)
		if localErr != nil {
			s.updatePackageStatus(pkg.ID, model.PackageStatusFailed, 0, fmt.Sprintf("COS上传失败且本地存储失败: %v", localErr))
			return "", fmt.Errorf("存储失败: %w", localErr)
		}

		// 更新数据库记录为本地存储
		s.db.Model(pkg).Updates(map[string]interface{}{
			"c_cos_bucket": "local",
			"c_cos_path":   localPath,
		})
		log.InfoContextf(ctx, "[uploadToCOS] 降级到本地存储成功, path: %s", localPath)
		return localPath, nil
	}

	log.InfoContextf(ctx, "[uploadToCOS] COS上传成功, location: %s", cosResp.Location)
	return cosResp.Location, nil
}

// saveToLocal 保存到本地
func (s *FunctionPackageService) saveToLocal(ctx context.Context, pkg *model.FunctionPackage, fileContent []byte, storageConfig *StorageConfig) (string, error) {
	log.InfoContextf(ctx, "[saveToLocal] 开始保存到本地, path: %s", storageConfig.Path)
	
	localPath, err := s.saveToLocalFile(fileContent, storageConfig.Path)
	if err != nil {
		s.updatePackageStatus(pkg.ID, model.PackageStatusFailed, 0, fmt.Sprintf("本地存储失败: %v", err))
		return "", fmt.Errorf("本地存储失败: %w", err)
	}

	// 更新数据库记录
	s.db.Model(pkg).Update("c_cos_path", localPath)
	log.InfoContextf(ctx, "[saveToLocal] 本地存储成功, path: %s", localPath)
	return localPath, nil
}

// finalizeUpload 完成上传并更新状态
func (s *FunctionPackageService) finalizeUpload(ctx context.Context, pkg *model.FunctionPackage, cosURL string) error {
	log.InfoContextf(ctx, "[finalizeUpload] 开始完成上传, ID: %d", pkg.ID)
	
	// 更新数据库状态
	err := s.updatePackageStatus(pkg.ID, model.PackageStatusAvailable, 100, "")
	if err != nil {
		return fmt.Errorf("更新状态失败: %w", err)
	}

	// 更新访问URL
	if cosURL != "" {
		s.db.Model(pkg).Update("c_cos_url", cosURL)
	}

	log.InfoContextf(ctx, "[finalizeUpload] 上传完成, ID: %d, URL: %s", pkg.ID, cosURL)
	return nil
}
