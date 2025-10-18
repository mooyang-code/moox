package logic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	cloudAccountModel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"
)

// getCOSConfigFromAccount 从云账户获取COS配置
func (s *FunctionPackageService) getCOSConfigFromAccount(ctx context.Context, accountID string) (*cloudAccountModel.CloudAccount, bool, error) {
	account, err := s.dao.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, false, fmt.Errorf("云账户不存在: %w", err)
	}

	// 检查COS配置是否完整
	hasCOSConfig := account.COSRegion != "" && account.COSBucket != ""
	return account, hasCOSConfig, nil
}

// saveToLocalFile 保存文件到本地存储目录
func (s *FunctionPackageService) saveToLocalFile(content []byte, filename string) (string, error) {
	filePath := constants.GetPackageStorageFilePath(filename)

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

// generateCOSPath 生成COS文件路径
func (s *FunctionPackageService) generateCOSPath(packageType, packageName, version string) string {
	timestamp := time.Now().Unix()
	filename := fmt.Sprintf("%s-%s-%d.zip", packageName, version, timestamp)

	// 数据采集器类型：直接在data_collector下按版本存储
	if packageType == model.PackageTypeDataCollector {
		return fmt.Sprintf("%s/%s/%s", packageType, version, filename)
	}

	// 因子计算器类型：在factor_calculator下按具体因子名称和版本存储
	return fmt.Sprintf("%s/%s/%s/%s", packageType, packageName, version, filename)
}

// calculateMD5 计算内容的MD5值
func (s *FunctionPackageService) calculateMD5(content []byte) string {
	hash := md5.Sum(content)
	return hex.EncodeToString(hash[:])
}

// createCOSClient 创建COS客户端
func (s *FunctionPackageService) createCOSClient(_ context.Context, account *cloudAccountModel.CloudAccount) (provider.COS, error) {
	// 解析云平台类型
	platformType, err := provider.ParseCloudPlatform(account.Provider)
	if err != nil {
		return nil, fmt.Errorf("不支持的云平台类型: %w", err)
	}

	// 构建配置
	extraConfig := fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_app_id":"%s"}`,
		account.COSRegion, account.COSBucket, account.AppID)

	// 创建云平台配置
	config, err := provider.NewConfig(platformType, account.SecretID, account.SecretKey, extraConfig)
	if err != nil {
		return nil, fmt.Errorf("创建云配置失败: %w", err)
	}

	// 使用工厂方法创建支持COS的云厂商客户端
	cosProvider, err := provider.NewWithCOS(config)
	if err != nil {
		return nil, fmt.Errorf("创建COS客户端失败: %w", err)
	}

	return cosProvider, nil
}

// determineStorageStrategy 确定存储策略
func (s *FunctionPackageService) determineStorageStrategy(ctx context.Context, req *UploadPackageRequest) (*StorageConfig, error) {
	// 如果没有指定云账户ID，使用本地存储
	if req.CloudAccountID == "" {
		return &StorageConfig{
			UseCOS: false,
		}, nil
	}

	// 获取云账户COS配置
	account, hasCOSConfig, err := s.getCOSConfigFromAccount(ctx, req.CloudAccountID)
	if err != nil {
		return nil, err
	}

	if !hasCOSConfig {
		return nil, fmt.Errorf("云账户未配置COS")
	}

	// 生成COS路径
	cosPath := s.generateCOSPath(req.PackageType, req.PackageName, req.Version)

	return &StorageConfig{
		UseCOS: true,
		Bucket: account.COSBucket,
		Path:   cosPath,
	}, nil
}
