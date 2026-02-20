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
		BizType:             dto.BizType,
		Region:              dto.Region,
		Tag:                 dto.Tag,
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
		BizType:             node.BizType,
		Region:              node.Region,
		Tag:                 node.Tag,
		IPAddress:           node.IPAddress,
		SupportedCollectors: node.SupportedCollectors,
		Metadata:            node.Metadata,
		TimeoutThreshold:    node.TimeoutThreshold,
		HeartbeatInterval:   node.HeartbeatInterval,
		ProbeEnabled:        node.ProbeEnabled,
		ProbeURL:            node.ProbeURL,
		LastHeartbeat:       node.LastHeartbeat,
		Invalid:             node.Invalid,
		CreateTime:          node.CreateTime,
		ModifyTime:          node.ModifyTime,
	}
	return dto
}

// convertToCloudNodeDTOWithHeartbeat 将model转换为CloudNodeDTO，并从内存获取心跳数据
func (s *ServiceImpl) convertToCloudNodeDTOWithHeartbeat(node *model.CloudNode) *CloudNodeDTO {
	dto := s.ConvertToCloudNodeDTO(node)
	if dto == nil {
		return nil
	}

	// 从内存获取心跳信息
	if s.heartbeatStore != nil {
		lastHeartbeat := s.heartbeatStore.GetLastHeartbeat(node.NodeID)
		if lastHeartbeat != nil {
			dto.LastHeartbeat = lastHeartbeat
		}

		// 计算节点状态
		status := s.heartbeatStore.GetNodeStatus(node.NodeID, node.TimeoutThreshold)
		dto.Status = &status
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
func (s *ServiceImpl) generateNodeID(ctx context.Context, region string, bizType string) (string, error) {
	// 根据业务类型确定节点ID中的标记名
	bizLabel := model.BizTypeLabel(bizType)

	// 检查该region是否已有同业务类型的Master节点
	nodes, err := s.nodeDAO.GetCloudNodesByRegion(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to check existing nodes: %w", err)
	}

	// 检查是否已有Master节点
	masterPrefix := fmt.Sprintf("%s-Master-", bizLabel)
	hasMaster := false
	for _, node := range nodes {
		if strings.Contains(node.NodeID, masterPrefix) {
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
		nodeID = fmt.Sprintf("scf%s-%s-Master-%d", randomStr, bizLabel, timestamp)
	} else {
		nodeID = fmt.Sprintf("scf%s-%s-General-%d", randomStr, bizLabel, timestamp)
	}
	return nodeID, nil
}

// ========== 命名空间分配相关方法 ==========

// generateNamespaceID 生成命名空间ID
// 格式：地区ID-3位随机字符串，如 ap-guangzhou-abc
func (s *ServiceImpl) generateNamespaceID(region string) string {
	randomStr := common.GenerateID(3)
	return fmt.Sprintf("%s-%s", region, randomStr)
}

// getNamespaceUsage 获取指定region下各命名空间的节点使用数量
func (s *ServiceImpl) getNamespaceUsage(ctx context.Context, region string) (map[string]int, error) {
	usage, err := s.nodeDAO.GetNamespaceStats(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace stats: %w", err)
	}
	if usage == nil {
		usage = make(map[string]int)
	}
	return usage, nil
}

// syncCloudNamespaces 从云端获取命名空间列表，同步到usage map
// 云端存在但本地无记录的命名空间会被添加到usage中（节点数为0）
func (s *ServiceImpl) syncCloudNamespaces(ctx context.Context, client provider.Client, region string, usage map[string]int) error {
	namespaces, err := client.ListNamespaces(ctx, region)
	if err != nil {
		return fmt.Errorf("failed to list namespaces from cloud: %w", err)
	}
	for _, ns := range namespaces {
		if ns.Name == "" {
			continue
		}
		log.InfoContextf(ctx, "[CloudNode] Found cloud namespace: %s", ns.Name)
		if _, exists := usage[ns.Name]; !exists {
			usage[ns.Name] = 0
		}
	}
	return nil
}

// ensureNamespacesExist 确保命名空间数量达到maxCount，不足时创建新的
// 返回新创建的命名空间列表
func (s *ServiceImpl) ensureNamespacesExist(ctx context.Context, client provider.Client, region string, currentCount, maxCount int) ([]string, error) {
	var created []string
	needCreate := maxCount - currentCount
	if needCreate <= 0 {
		return created, nil
	}

	for i := 0; i < needCreate; i++ {
		nsID := s.generateNamespaceID(region)
		if err := client.CreateNamespace(ctx, nsID, "MooX Auto Created", region); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] Failed to create namespace %s: %v", nsID, err)
			continue
		}
		created = append(created, nsID)
		log.InfoContextf(ctx, "[CloudNode] Created namespace: %s", nsID)
	}
	return created, nil
}

// findAvailableNamespace 从usage中查找一个空闲的命名空间
// 返回节点数最少且小于maxFunctions的命名空间
func (s *ServiceImpl) findAvailableNamespace(usage map[string]int, maxFunctions int) (string, error) {
	var selected string
	minCount := maxFunctions

	for ns, count := range usage {
		if count >= maxFunctions {
			continue
		}
		if count < minCount {
			minCount = count
			selected = ns
		}
	}

	if selected == "" {
		return "", fmt.Errorf("no available namespace found")
	}
	return selected, nil
}

// getNamespaceLimits 获取命名空间限制配置
func (s *ServiceImpl) getNamespaceLimits(ctx context.Context, cloudAccountID, region string) (maxNamespaces, maxFunctions int) {
	// 默认值
	maxNamespaces = 5
	maxFunctions = 50

	if s.config == nil {
		return
	}

	info, err := s.getRegionInfoByAccount(ctx, cloudAccountID, region)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] Failed to get region limits: %v", err)
		return
	}

	if info.MaxNamespacesPerRegion > 0 {
		maxNamespaces = info.MaxNamespacesPerRegion
	}
	if info.MaxFunctionsPerNamespace > 0 {
		maxFunctions = info.MaxFunctionsPerNamespace
	}
	return
}

// getAvailableNamespace 获取一个可用的命名空间
// 1. 获取本地命名空间使用情况
// 2. 同步云端命名空间
// 3. 补齐命名空间数量（不足时自动创建）
// 4. 查找空闲命名空间返回
func (s *ServiceImpl) getAvailableNamespace(ctx context.Context, cloudAccountID string, region string, client provider.Client) (string, error) {
	if client == nil {
		return "", fmt.Errorf("cloud provider client is required")
	}

	// 1. 获取配置限制
	maxNamespaces, maxFunctions := s.getNamespaceLimits(ctx, cloudAccountID, region)
	if maxNamespaces <= 0 || maxFunctions <= 0 {
		return "", fmt.Errorf("invalid namespace limits: maxNamespaces=%d, maxFunctions=%d", maxNamespaces, maxFunctions)
	}

	// 2. 获取本地命名空间使用情况
	usage, err := s.getNamespaceUsage(ctx, region)
	if err != nil {
		return "", err
	}

	// 3. 同步云端命名空间到usage
	if err := s.syncCloudNamespaces(ctx, client, region, usage); err != nil {
		return "", err
	}

	// 4. 补齐命名空间（如果不足）
	if len(usage) < maxNamespaces {
		newNamespaces, _ := s.ensureNamespacesExist(ctx, client, region, len(usage), maxNamespaces)
		for _, ns := range newNamespaces {
			usage[ns] = 0
		}
	}

	// 5. 查找空闲命名空间
	selected, err := s.findAvailableNamespace(usage, maxFunctions)
	if err != nil {
		if len(usage) >= maxNamespaces {
			return "", fmt.Errorf("namespace limit reached in region %s", region)
		}
		return "", fmt.Errorf("no available namespace in region %s", region)
	}

	return selected, nil
}
