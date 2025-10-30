package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/common"
	authutils "github.com/mooyang-code/moox/server/internal/service/auth/utils"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/fileserver"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 辅助方法 ==========

// generateDisplayFilename 生成显示文件名
func (s *ServiceImpl) generateDisplayFilename(pkg *model.FunctionPackage) string {
	if pkg.PackageName != "" && pkg.Version != "" {
		return fmt.Sprintf("%s_%s.zip", pkg.PackageName, pkg.Version)
	}
	return pkg.OriginalFilename
}

// determineLocalFilePath 确定本地文件路径
func (s *ServiceImpl) determineLocalFilePath(pkg *model.FunctionPackage) string {
	if pkg.COSPath != "" {
		return s.resolvePathFromCOSPath(pkg.COSPath)
	}
	return s.buildLocalPathFromPackage(pkg)
}

// resolvePathFromCOSPath 从COSPath解析本地路径
func (s *ServiceImpl) resolvePathFromCOSPath(cosPath string) string {
	if filepath.IsAbs(cosPath) {
		return cosPath
	}
	return constants.GetPackageStorageFilePath(cosPath)
}

// buildLocalPathFromPackage 从包信息构建本地路径
func (s *ServiceImpl) buildLocalPathFromPackage(pkg *model.FunctionPackage) string {
	if pkg.OriginalFilename != "" {
		return constants.GetPackageStorageFilePath(pkg.OriginalFilename)
	}
	// 使用包名和版本构建文件名
	localFileName := fmt.Sprintf("%s_%s.zip", pkg.PackageName, pkg.Version)
	return constants.GetPackageStorageFilePath(localFileName)
}

// ensureFileAvailable 确保文件在本地可用并返回下载URL
func (s *ServiceImpl) ensureFileAvailable(ctx context.Context, pkg *model.FunctionPackage, localFilePath string) (string, error) {
	// 检查文件是否已存在于本地
	if s.isFileExists(localFilePath) {
		log.InfoContextf(ctx, "[ensureFileAvailable] 文件已存在于本地: %s", localFilePath)
		return s.buildDownloadURL(ctx, localFilePath), nil
	}

	// 文件不存在，从COS下载到本地
	log.InfoContextf(ctx, "[ensureFileAvailable] 文件不在本地，开始从COS下载: %s", localFilePath)
	if err := s.downloadFromCOSIfPossible(ctx, pkg, localFilePath); err != nil {
		return "", err
	}
	log.InfoContextf(ctx, "[ensureFileAvailable] 文件下载完成: %s", localFilePath)
	return s.buildDownloadURL(ctx, localFilePath), nil
}

// isFileExists 检查文件是否存在
func (s *ServiceImpl) isFileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// buildDownloadURL 构建带JWT令牌的下载URL
func (s *ServiceImpl) buildDownloadURL(ctx context.Context, localFilePath string) string {
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
	token, err := fileserver.GenerateFileDownloadToken(userID, relativeFilePath, 30*time.Minute)
	if err != nil {
		log.ErrorContextf(ctx, "[buildDownloadURL] 生成JWT令牌失败: %v", err)
		// 如果生成令牌失败，返回不带令牌的URL（降级处理）
		return fmt.Sprintf("/files/%s", relativeFilePath)
	}

	// 返回带JWT令牌的URL
	return fmt.Sprintf("/files/%s?token=%s", relativeFilePath, token)
}

// downloadFromCOSIfPossible 如果可能的话从COS下载文件
func (s *ServiceImpl) downloadFromCOSIfPossible(ctx context.Context, pkg *model.FunctionPackage, localFilePath string) error {
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
func (s *ServiceImpl) canDownloadFromCOS(pkg *model.FunctionPackage) bool {
	return pkg.COSBucket != "local" && pkg.CloudAccountID != ""
}

// downloadFromCOSToLocal 从COS下载代码包到本地
func (s *ServiceImpl) downloadFromCOSToLocal(ctx context.Context, pkg *model.FunctionPackage, localPath string) error {
	// 1. 确保本地目录存在
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("创建本地目录失败: %w", err)
	}

	// 2. 获取 COS 账户信息
	account, err := s.accountDAO.GetCloudAccount(ctx, pkg.CloudAccountID)
	if err != nil {
		return fmt.Errorf("获取云账户信息失败: %w", err)
	}
	accountInfo := &COSAccountInfo{
		Provider:  account.Provider,
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		AppID:     account.AppID,
		COSRegion: account.COSRegion,
		COSBucket: account.COSBucket,
	}

	// 3. 创建专用的COS客户端
	cosClient, err := s.createCOSClient(ctx, accountInfo)
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
func (s *ServiceImpl) extractUserIDFromContext(ctx context.Context) string {
	return authutils.GetUserIDFromContext(ctx)
}

// createCOSClient 创建COS客户端（用于下载功能）
func (s *ServiceImpl) createCOSClient(_ context.Context, account *COSAccountInfo) (provider.Client, error) {
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
	cosProvider, err := provider.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("创建COS客户端失败: %w", err)
	}

	return cosProvider, nil
}

// ========== 转换函数 ==========

// cloudNodeDTOToModel 将CloudNodeDTO转换为model.CloudNode
func cloudNodeDTOToModel(dto *CloudNodeDTO) *model.CloudNode {
	if dto == nil {
		return nil
	}
	return &model.CloudNode{
		ID:                  dto.ID,
		NodeID:              dto.NodeID,
		CloudAccountID:      dto.CloudAccountID,
		PackageID:           dto.PackageID,
		Namespace:           dto.Namespace,
		NodeType:            dto.NodeType,
		Region:              dto.Region,
		IPAddress:           dto.IPAddress,
		SupportedCollectors: dto.SupportedCollectors,
		Metadata:            dto.Metadata,
		TimeoutThreshold:    dto.TimeoutThreshold,
		HeartbeatInterval:   dto.HeartbeatInterval,
		ProbeEnabled:        dto.ProbeEnabled,
		ProbeURL:            dto.ProbeURL,
		Invalid:             dto.Invalid,
		CreateTime:          dto.CreateTime,
		ModifyTime:          dto.ModifyTime,
	}
}

// cloudAccountDTOToModel 将CloudAccountDTO转换为model.CloudAccount
func cloudAccountDTOToModel(dto *CloudAccountDTO) *model.CloudAccount {
	if dto == nil {
		return nil
	}
	return &model.CloudAccount{
		ID:          dto.ID,
		AccountID:   dto.AccountID,
		AccountName: dto.AccountName,
		Provider:    dto.Provider,
		SecretID:    dto.SecretID,
		SecretKey:   dto.SecretKey,
		AppID:       dto.AppID,
		COSRegion:   dto.COSRegion,
		COSBucket:   dto.COSBucket,
		ExtraConfig: dto.ExtraConfig,
		Invalid:     dto.Invalid,
		CreateTime:  dto.CreateTime,
		ModifyTime:  dto.ModifyTime,
	}
}

// ConvertToCloudNodeDTO 将model转换为CloudNodeDTO
func (s *ServiceImpl) ConvertToCloudNodeDTO(node *model.CloudNode) *CloudNodeDTO {
	if node == nil {
		return nil
	}

	dto := &CloudNodeDTO{
		ID:                  node.ID,
		NodeID:              node.NodeID,
		CloudAccountID:      node.CloudAccountID,
		PackageID:           node.PackageID,
		PackageVersion:      node.PackageVersion,
		Namespace:           node.Namespace,
		NodeType:            node.NodeType,
		Region:              node.Region,
		IPAddress:           node.IPAddress,
		SupportedCollectors: node.SupportedCollectors,
		Metadata:            node.Metadata,
		TimeoutThreshold:    node.TimeoutThreshold,
		HeartbeatInterval:   node.HeartbeatInterval,
		ProbeEnabled:        node.ProbeEnabled,
		ProbeURL:            node.ProbeURL,
		Invalid:             node.Invalid,
		CreateTime:          node.CreateTime,
		ModifyTime:          node.ModifyTime,
	}
	return dto
}

// ConvertToCloudAccountDTO 将model转换为CloudAccountDTO（脱敏处理）
func (s *ServiceImpl) ConvertToCloudAccountDTO(account *model.CloudAccount) *CloudAccountDTO {
	if account == nil {
		return nil
	}

	dto := &CloudAccountDTO{
		ID:          account.ID,
		AccountID:   account.AccountID,
		AccountName: account.AccountName,
		Provider:    account.Provider,
		AppID:       account.AppID,
		COSRegion:   account.COSRegion,
		COSBucket:   account.COSBucket,
		ExtraConfig: account.ExtraConfig,
		Invalid:     account.Invalid,
		CreateTime:  account.CreateTime,
		ModifyTime:  account.ModifyTime,
	}

	// 脱敏处理
	if account.SecretID != "" {
		dto.SecretID = account.SecretID[:4] + "****"
	}
	if account.SecretKey != "" {
		dto.SecretKey = "****"
	}
	return dto
}

// ========== 验证和生成工具方法 ==========

// validateJSONField 验证JSON字段格式
func (s *ServiceImpl) validateJSONField(field string, fieldName string) error {
	if field == "" {
		return nil
	}

	var temp interface{}
	if err := json.Unmarshal([]byte(field), &temp); err != nil {
		return fmt.Errorf("invalid %s format: %w", fieldName, err)
	}
	return nil
}

// generateNodeID 生成节点ID
func (s *ServiceImpl) generateNodeID(ctx context.Context, region string) (string, error) {
	// 检查该region是否已有Master节点
	nodes, err := s.nodeDAO.GetCloudNodesByRegion(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to check existing nodes: %w", err)
	}

	// 检查是否已有Master节点
	hasMaster := false
	for _, node := range nodes {
		if strings.HasPrefix(node.NodeID, "DataCollector-Master-") {
			hasMaster = true
			break
		}
	}

	// 生成Unix时间戳
	timestamp := time.Now().Unix()

	// 生成更随机的字符串
	randomStr := common.GenerateID(4)

	// 根据是否已有Master节点决定节点类型
	var nodeID string
	if !hasMaster {
		nodeID = fmt.Sprintf("DataCollector-Master-%d-%s", timestamp, randomStr)
	} else {
		nodeID = fmt.Sprintf("DataCollector-General-%d-%s", timestamp, randomStr)
	}
	return nodeID, nil
}

// allocateNamespace 分配命名空间
func (s *ServiceImpl) allocateNamespace(ctx context.Context, region string) (string, error) {
	// 获取该region下的所有命名空间使用情况
	namespaceStats, err := s.nodeDAO.GetNamespaceStats(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to get namespace stats: %w", err)
	}

	// 查找可用的命名空间
	// 规则：每个命名空间最多50个函数，每个region最多5个命名空间
	for i := 1; i <= 5; i++ {
		namespace := fmt.Sprintf("%s-%02d", region, i)
		count, exists := namespaceStats[namespace]

		// 如果命名空间不存在或函数数少于50个，则可以使用
		if !exists || count < 50 {
			return namespace, nil
		}
	}

	// 如果所有命名空间都满了，尝试找到函数数最少的命名空间
	minCount := 50
	selectedNamespace := ""
	for i := 1; i <= 5; i++ {
		namespace := fmt.Sprintf("%s-%02d", region, i)
		count, exists := namespaceStats[namespace]
		if !exists {
			// 如果命名空间不存在，直接使用
			return namespace, nil
		}
		if count < minCount {
			minCount = count
			selectedNamespace = namespace
		}
	}

	if selectedNamespace != "" {
		return selectedNamespace, nil
	}
	return "", fmt.Errorf("no available namespace in region %s", region)
}
