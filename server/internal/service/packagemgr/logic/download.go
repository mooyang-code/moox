package logic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	authutils "github.com/mooyang-code/moox/server/internal/service/auth/utils"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"trpc.group/trpc-go/trpc-go/log"
)

// GetPackageDownloadURL 获取代码包下载URL（带JWT认证）
func (s *FunctionPackageService) GetPackageDownloadURL(ctx context.Context, id int64) (*PackageDownloadURL, error) {
	// 查询代码包信息
	pkg, err := s.dao.GetByID(ctx, id)
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
		ID:          pkg.ID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Filename:    filename,
		DownloadURL: downloadURL,
		FileSize:    pkg.FileSize,
		FileMD5:     pkg.FileMD5,
	}, nil
}

// generateDisplayFilename 生成显示文件名
func (s *FunctionPackageService) generateDisplayFilename(pkg *model.FunctionPackage) string {
	if pkg.PackageName != "" && pkg.Version != "" {
		return fmt.Sprintf("%s_%s.zip", pkg.PackageName, pkg.Version)
	}
	return pkg.OriginalFilename
}

// determineLocalFilePath 确定本地文件路径
func (s *FunctionPackageService) determineLocalFilePath(pkg *model.FunctionPackage) string {
	if pkg.COSPath != "" {
		return s.resolvePathFromCOSPath(pkg.COSPath)
	}
	return s.buildLocalPathFromPackage(pkg)
}

// resolvePathFromCOSPath 从COSPath解析本地路径
func (s *FunctionPackageService) resolvePathFromCOSPath(cosPath string) string {
	if filepath.IsAbs(cosPath) {
		return cosPath
	}
	return constants.GetPackageStorageFilePath(cosPath)
}

// buildLocalPathFromPackage 从包信息构建本地路径
func (s *FunctionPackageService) buildLocalPathFromPackage(pkg *model.FunctionPackage) string {
	if pkg.OriginalFilename != "" {
		return constants.GetPackageStorageFilePath(pkg.OriginalFilename)
	}
	// 使用包名和版本构建文件名
	localFileName := fmt.Sprintf("%s_%s.zip", pkg.PackageName, pkg.Version)
	return constants.GetPackageStorageFilePath(localFileName)
}

// ensureFileAvailable 确保文件在本地可用并返回下载URL
func (s *FunctionPackageService) ensureFileAvailable(ctx context.Context, pkg *model.FunctionPackage, localFilePath string) (string, error) {
	// 检查文件是否已存在于本地
	if s.isFileExists(localFilePath) {
		return s.buildDownloadURL(ctx, localFilePath), nil
	}

	// 文件不存在，尝试从COS下载
	if err := s.downloadFromCOSIfPossible(ctx, pkg, localFilePath); err != nil {
		return "", err
	}
	return s.buildDownloadURL(ctx, localFilePath), nil
}

// isFileExists 检查文件是否存在
func (s *FunctionPackageService) isFileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// buildDownloadURL 构建带JWT令牌的下载URL
func (s *FunctionPackageService) buildDownloadURL(ctx context.Context, localFilePath string) string {
	baseDir := constants.GetPackageStorageDir()
	var relativeFilePath string

	if strings.HasPrefix(localFilePath, baseDir+"/") {
		relativeFilePath = strings.TrimPrefix(localFilePath, baseDir+"/")
	} else {
		relativeFilePath = filepath.Base(localFilePath)
	}

	// 获取用户ID
	userID := s.extractUserIDFromContext(ctx)

	// 生成JWT令牌（30分钟有效期）
	token, err := authutils.GenerateFileDownloadToken(userID, relativeFilePath, 30*time.Minute)
	if err != nil {
		log.ErrorContextf(ctx, "[buildDownloadURL] 生成JWT令牌失败: %v", err)
		// 如果生成令牌失败，返回不带令牌的URL（降级处理）
		return fmt.Sprintf("/files/%s", relativeFilePath)
	}

	// 返回带JWT令牌的URL
	return fmt.Sprintf("/files/%s?token=%s", relativeFilePath, token)
}

// downloadFromCOSIfPossible 如果可能的话从COS下载文件
func (s *FunctionPackageService) downloadFromCOSIfPossible(ctx context.Context, pkg *model.FunctionPackage, localFilePath string) error {
	if !s.canDownloadFromCOS(pkg) {
		return fmt.Errorf("本地文件不存在且无法从COS下载: %s (存储类型: %s, 云账户ID: %s)",
			localFilePath, pkg.COSBucket, pkg.CloudAccountID)
	}
	log.InfoContextf(ctx, "[GetPackageDownloadURL] 本地文件不存在，尝试从COS下载: %s", localFilePath)

	err := s.downloadFromCOSToLocal(ctx, pkg, localFilePath)
	if err != nil {
		return fmt.Errorf("从COS下载文件失败: %w", err)
	}
	log.InfoContextf(ctx, "[GetPackageDownloadURL] 成功从COS下载文件到本地: %s", localFilePath)
	return nil
}

// canDownloadFromCOS 检查是否可以从COS下载
func (s *FunctionPackageService) canDownloadFromCOS(pkg *model.FunctionPackage) bool {
	return pkg.COSBucket != "local" && pkg.CloudAccountID != ""
}

// downloadFromCOSToLocal 从COS下载代码包到本地
func (s *FunctionPackageService) downloadFromCOSToLocal(ctx context.Context, pkg *model.FunctionPackage, localPath string) error {
	// 1. 确保本地目录存在
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("创建本地目录失败: %w", err)
	}

	// 2. 获取云账户信息
	account, err := s.dao.GetCloudAccount(ctx, pkg.CloudAccountID)
	if err != nil {
		return fmt.Errorf("获取云账户信息失败: %w", err)
	}

	// 3. 创建专用的COS客户端
	cosClient, err := s.createCOSClient(ctx, account)
	if err != nil {
		return fmt.Errorf("创建COS客户端失败: %w", err)
	}

	// 4. 使用COS客户端下载文件
	err = cosClient.DownloadCOSToFile(ctx, pkg.COSPath, localPath)
	if err != nil {
		return fmt.Errorf("从COS下载文件失败: %w", err)
	}
	log.InfoContextf(ctx, "[downloadFromCOSToLocal] 成功从COS下载代码包到本地: %s -> %s", pkg.COSPath, localPath)
	return nil
}

// extractUserIDFromContext 从context中提取用户ID
func (s *FunctionPackageService) extractUserIDFromContext(ctx context.Context) string {
	return authutils.GetUserIDFromContext(ctx)
}
