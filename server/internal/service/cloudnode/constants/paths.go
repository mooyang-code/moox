package constants

import (
	"fmt"
	"path/filepath"
)

// 云函数相关路径常量定义
const (
	// DefaultTempDir 默认临时目录
	DefaultTempDir = "/tmp"

	// MooxSubDir moox子目录名
	MooxSubDir = "moox"

	// DefaultZipFileName 默认的云函数ZIP文件名
	DefaultZipFileName = "collector-scf.zip"

	// PackageFileNamePattern 代码包文件名模式（用于worker中的临时文件）
	PackageFileNamePattern = "package_%d_%s" // package_{ID}_{originalFilename}
)

// GetDefaultZipFilePath 获取默认的云函数ZIP文件完整路径
func GetDefaultZipFilePath() string {
	return filepath.Join(DefaultTempDir, DefaultZipFileName)
}

// GetPackageFilePath 根据包ID和原始文件名生成本地包文件路径（worker使用）
func GetPackageFilePath(packageID int64, originalFilename string) string {
	filename := fmt.Sprintf(PackageFileNamePattern, packageID, originalFilename)
	return filepath.Join(DefaultTempDir, filename)
}

// GetPackageStorageDir 获取包管理服务的存储目录
func GetPackageStorageDir() string {
	return filepath.Join(DefaultTempDir, MooxSubDir)
}

// GetPackageStorageFilePath 根据文件名生成包管理服务的存储文件路径
func GetPackageStorageFilePath(filename string) string {
	return filepath.Join(GetPackageStorageDir(), filename)
}
