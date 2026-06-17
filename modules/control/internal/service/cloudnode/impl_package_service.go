package cloudnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 代码包管理 ==========

// GetPackageList 获取代码包列表
func (s *ServiceImpl) GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error) {
	// 转换为dao请求
	daoReq := &dao.ListRequest{
		Page:        req.Page,
		PageSize:    req.PageSize,
		PackageName: req.PackageName,
		Runtime:     req.Runtime,
		PackageType: req.PackageType,
		BizType:     req.BizType,
		Status:      req.Status,
	}

	// 调用dao层获取数据
	packages, total, err := s.packageDAO.List(ctx, daoReq)
	if err != nil {
		return nil, fmt.Errorf("查询代码包列表失败: %w", err)
	}

	// 转换为VO
	items := make([]*PackageListItem, len(packages))
	for i, pkg := range packages {
		items[i] = &PackageListItem{
			PackageID:        pkg.PackageID,
			PackageName:      pkg.PackageName,
			Version:          pkg.Version,
			Description:      pkg.Description,
			Runtime:          pkg.Runtime,
			PackageType:      pkg.PackageType,
			PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),
			BizType:          pkg.BizType,
			FileSize:         pkg.FileSize,
			FileMD5:          pkg.FileMD5,
			CloudAccountID:   pkg.CloudAccountID,
			COSRegion:        pkg.COSRegion,
			Status:           pkg.Status,
			StatusLabel:      model.GetStatusDisplayName(pkg.Status),
			LastDeployTime:   pkg.LastDeployTime,
			CreateTime:       pkg.CreateTime,
		}
	}

	return &PackageListResponse{
		Total: total,
		Items: items,
	}, nil
}

// GetPackageDetail 获取代码包详情
func (s *ServiceImpl) GetPackageDetail(ctx context.Context, packageID string) (*PackageDetail, error) {
	pkg, err := s.packageDAO.GetByID(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("查询代码包详情失败: %w", err)
	}

	return s.ConvertToPackageDetail(pkg), nil
}

// ConvertToPackageDetail 将model转换为PackageDetail
func (s *ServiceImpl) ConvertToPackageDetail(pkg *model.FunctionPackage) *PackageDetail {
	return &PackageDetail{
		ID:               pkg.ID,
		PackageID:        pkg.PackageID,
		PackageName:      pkg.PackageName,
		Version:          pkg.Version,
		Description:      pkg.Description,
		Runtime:          pkg.Runtime,
		PackageType:      pkg.PackageType,
		PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),

		// 文件信息
		OriginalFilename: pkg.OriginalFilename,
		FileSize:         pkg.FileSize,
		FileMD5:          pkg.FileMD5,

		// COS存储信息
		CloudAccountID: pkg.CloudAccountID,
		COSRegion:      pkg.COSRegion,
		COSBucket:      pkg.COSBucket,
		COSPath:        pkg.COSPath,
		COSURL:         pkg.COSURL,

		// 状态管理
		Status:         pkg.Status,
		StatusLabel:    model.GetStatusDisplayName(pkg.Status),
		UploadProgress: pkg.UploadProgress,
		ErrorMessage:   pkg.ErrorMessage,

		// 使用统计
		LastDeployTime: pkg.LastDeployTime,

		// 审计字段
		Invalid:    pkg.Invalid,
		CreateTime: pkg.CreateTime,
		ModifyTime: pkg.ModifyTime,
	}
}

// DeletePackage 删除代码包（软删除，根据package_id字符串）
func (s *ServiceImpl) DeletePackage(ctx context.Context, packageID string) error {
	return s.packageDAO.Delete(ctx, packageID)
}

// UploadPackage 上传代码包（创建异步任务）
func (s *ServiceImpl) UploadPackage(ctx context.Context, req *UploadPackageRequest) (*UploadPackageResponse, error) {
	log.InfoContextf(ctx, "[UploadPackage] Creating async upload task: PackageName=%s, Version=%s, PackageType=%s, BizType=%s",
		req.PackageName, req.Version, req.PackageType, req.BizType)

	// 1. 先将文件内容保存到本地临时文件
	filePath, err := s.saveUploadFileToTemp(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("保存上传文件失败: %w", err)
	}
	log.InfoContextf(ctx, "[UploadPackage] Saved file to temp: %s", filePath)

	// 2. 构建异步任务请求参数（不包含文件内容，只包含文件路径）
	uploadFileReq := UploadPackageExecutorRequest{
		PackageName:    req.PackageName,
		Version:        req.Version,
		Description:    req.Description,
		Runtime:        req.Runtime,
		PackageType:    req.PackageType,
		BizType:        req.BizType,
		CloudAccountID: req.CloudAccountID,
		FilePath:       filePath, // 使用文件路径替代文件内容
	}

	// 将请求参数序列化为JSON
	requestParams, err := json.Marshal(uploadFileReq)
	if err != nil {
		return nil, fmt.Errorf("序列化请求参数失败: %w", err)
	}

	// 创建异步任务（包含一个上传任务）
	tasks := []asynctask.TaskRequest{
		{
			TaskType:      asynctask.TaskTypeUploadFileToCOS,
			RequestParams: string(requestParams),
		},
	}

	jobID, err := s.asyncTask.AsyncJobCreate(ctx, tasks)
	if err != nil {
		return nil, fmt.Errorf("创建异步任务失败: %w", err)
	}

	log.InfoContextf(ctx, "[UploadPackage] Created async upload job: JobID=%s", jobID)

	// 返回任务信息
	return &UploadPackageResponse{
		JobID:       jobID,
		PackageName: req.PackageName,
		Version:     req.Version,
		Status:      0, // 任务已创建，等待处理
		Message:     "文件上传任务已创建，正在处理中...",
	}, nil
}

// ========== 代码包下载 ==========

// saveUploadFileToTemp 将上传的文件内容保存到本地临时文件
func (s *ServiceImpl) saveUploadFileToTemp(ctx context.Context, req *UploadPackageRequest) (string, error) {
	// 解码base64文件内容
	fileContent, err := base64.StdEncoding.DecodeString(req.FileContent)
	if err != nil {
		return "", fmt.Errorf("解码base64文件内容失败: %w", err)
	}

	// 生成临时文件名（基于包名、版本和时间戳）
	timestamp := time.Now().Unix()
	packageID := model.GeneratePackageID()
	filename := fmt.Sprintf("upload_%s_%s_%d_%s.zip", req.PackageName, req.Version, timestamp, packageID)
	
	// 使用 constants 提供的路径方法
	filePath := constants.GetPackageStorageFilePath(filename)
	
	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, fileContent, 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	log.InfoContextf(ctx, "[SaveUploadFile] Saved file: %s, size: %d bytes", filePath, len(fileContent))
	return filePath, nil
}

// ========== 代码包下载 ==========

// GetPackageDownloadURL 获取代码包下载URL（带JWT认证，根据package_id字符串）
func (s *ServiceImpl) GetPackageDownloadURL(ctx context.Context, packageID string) (*PackageDownloadURL, error) {
	// 查询代码包信息
	pkg, err := s.packageDAO.GetByID(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("查询代码包失败: %w", err)
	}

	// 生成文件名
	filename := s.generateDisplayFilename(pkg)

	// 确定本地文件路径
	localFilePath := s.determineLocalFilePath(pkg)

	// 确保文件在本地可用
	downloadURL, err := s.ensureFileAvailable(ctx, pkg, localFilePath)
	if err != nil {
		return nil, err
	}

	return &PackageDownloadURL{
		PackageID:   pkg.PackageID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Filename:    filename,
		DownloadURL: downloadURL,
		FileSize:    pkg.FileSize,
		FileMD5:     pkg.FileMD5,
	}, nil
}
