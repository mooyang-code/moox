package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	authutils "github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/modules/admin/internal/service/fileserver"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"

	"google.golang.org/protobuf/types/known/structpb"
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
		return fmt.Sprintf("/api/admin/fileserver/download?file=%s", relativeFilePath)
	}

	// 返回统一网关下载路径（file_download token 与 file 均通过 query 传递，浏览器 <a> 可携带）
	return fmt.Sprintf("/api/admin/fileserver/download?file=%s&token=%s", relativeFilePath, token)
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
//
// model ↔ PB 转换：service 实现内部一次性转换，dao 层仍返回 model（带 sql tag）。
// status 用 string 表达（"online"/"offline"/"timeout"/"abnormal"），由调用方按需计算填充。

// cloudNodePBToModel 将 pb.CloudNode 转换为 model.CloudNode（用于写入 DB）。
func cloudNodePBToModel(p *pb.CloudNode) *model.CloudNode {
	if p == nil {
		return nil
	}
	return &model.CloudNode{
		ID:                  int(p.GetId()),
		NodeID:              p.GetNodeId(),
		CloudAccountID:      p.GetCloudAccountId(),
		PackageID:           p.GetPackageId(),
		Namespace:           p.GetNamespace(),
		NodeType:            p.GetNodeType(),
		BizType:             p.GetBizType(),
		Region:              p.GetRegion(),
		Tag:                 p.GetTag(),
		IPAddress:           p.GetIpAddress(),
		SupportedCollectors: p.GetSupportedCollectors(),
		Metadata:            p.GetMetadata(),
		TimeoutThreshold:    int(p.GetTimeoutThreshold()),
		HeartbeatInterval:   int(p.GetHeartbeatInterval()),
		ProbeEnabled:        p.GetProbeEnabled(),
		ProbeURL:            p.GetProbeUrl(),
		RunningVersion:      p.GetRunningVersion(),
		IsDeleted:           p.GetIsDeleted(),
		CreateTime:          parseTime(p.GetCreateTime()),
		ModifyTime:          parseTime(p.GetModifyTime()),
	}
}

// cloudAccountPBToModel 将 pb.CloudAccount 转换为 model.CloudAccount（用于写入 DB）。
func cloudAccountPBToModel(p *pb.CloudAccount) *model.CloudAccount {
	if p == nil {
		return nil
	}
	return &model.CloudAccount{
		ID:          int(p.GetId()),
		AccountID:   p.GetAccountId(),
		AccountName: p.GetAccountName(),
		Provider:    p.GetProvider(),
		SecretID:    p.GetSecretId(),
		SecretKey:   p.GetSecretKey(),
		AppID:       p.GetAppId(),
		COSRegion:   p.GetCosRegion(),
		COSBucket:   p.GetCosBucket(),
		ExtraConfig: p.GetExtraConfig(),
		IsDeleted:   p.GetIsDeleted(),
	}
}

// cloudNodeModelToPB 将 model.CloudNode 转换为 pb.CloudNode（用于返回前端）。
// lastHeartbeat 为内存心跳（可为 nil）；status 由调用方按心跳计算后填充。
func cloudNodeModelToPB(node *model.CloudNode) *pb.CloudNode {
	if node == nil {
		return nil
	}
	return &pb.CloudNode{
		Id:                  int32(node.ID),
		NodeId:              node.NodeID,
		CloudAccountId:      node.CloudAccountID,
		PackageId:           node.PackageID,
		RunningVersion:      node.RunningVersion,
		Namespace:           node.Namespace,
		NodeType:            node.NodeType,
		BizType:             node.BizType,
		Region:              node.Region,
		Tag:                 node.Tag,
		IpAddress:           node.IPAddress,
		SupportedCollectors: node.SupportedCollectors,
		Metadata:            node.Metadata,
		TimeoutThreshold:    int32(node.TimeoutThreshold),
		HeartbeatInterval:   int32(node.HeartbeatInterval),
		ProbeEnabled:        node.ProbeEnabled,
		ProbeUrl:            node.ProbeURL,
		IsDeleted:           node.IsDeleted,
		LastHeartbeat:       formatTime(node.LastHeartbeat),
		CreateTime:          formatTime(node.CreateTime),
		ModifyTime:          formatTime(node.ModifyTime),
	}
}

// cloudAccountModelToPB 将 model.CloudAccount 转换为 pb.CloudAccount（脱敏）。
func cloudAccountModelToPB(account *model.CloudAccount) *pb.CloudAccount {
	if account == nil {
		return nil
	}
	p := &pb.CloudAccount{
		Id:          int32(account.ID),
		AccountId:   account.AccountID,
		AccountName: account.AccountName,
		Provider:    account.Provider,
		AppId:       account.AppID,
		CosRegion:   account.COSRegion,
		CosBucket:   account.COSBucket,
		ExtraConfig: account.ExtraConfig,
		IsDeleted:   account.IsDeleted,
		CreateTime:  formatTime(account.CreateTime),
		ModifyTime:  formatTime(account.ModifyTime),
	}
	// 脱敏
	if account.SecretID != "" {
		p.SecretId = account.SecretID[:4] + "****"
	}
	if account.SecretKey != "" {
		p.SecretKey = "****"
	}
	return p
}

// packageListItemModelToPB 将 model.FunctionPackage 转换为 pb.PackageListItem。
func packageListItemModelToPB(pkg *model.FunctionPackage) *pb.PackageListItem {
	if pkg == nil {
		return nil
	}
	return &pb.PackageListItem{
		PackageId:        pkg.PackageID,
		PackageName:      pkg.PackageName,
		Version:          pkg.Version,
		Description:      pkg.Description,
		Runtime:          pkg.Runtime,
		PackageType:      pkg.PackageType,
		PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),
		BizType:          pkg.BizType,
		FileSize:         pkg.FileSize,
		FileMd5:          pkg.FileMD5,
		CloudAccountId:   pkg.CloudAccountID,
		CosRegion:        pkg.COSRegion,
		Status:           int32(pkg.Status),
		StatusLabel:      model.GetStatusDisplayName(pkg.Status),
		LastDeployTime:   formatTime(pkg.LastDeployTime),
		CreatedTime:      formatTime(pkg.CreateTime),
	}
}

// packageDetailModelToPB 将 model.FunctionPackage 转换为 pb.PackageDetail。
func packageDetailModelToPB(pkg *model.FunctionPackage) *pb.PackageDetail {
	if pkg == nil {
		return nil
	}
	return &pb.PackageDetail{
		Id:               pkg.ID,
		PackageId:        pkg.PackageID,
		PackageName:      pkg.PackageName,
		Version:          pkg.Version,
		Description:      pkg.Description,
		Runtime:          pkg.Runtime,
		PackageType:      pkg.PackageType,
		PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),
		OriginalFilename: pkg.OriginalFilename,
		FileSize:         pkg.FileSize,
		FileMd5:          pkg.FileMD5,
		CloudAccountId:   pkg.CloudAccountID,
		CosRegion:        pkg.COSRegion,
		CosBucket:        pkg.COSBucket,
		CosPath:          pkg.COSPath,
		CosUrl:           pkg.COSURL,
		Status:           int32(pkg.Status),
		StatusLabel:      model.GetStatusDisplayName(pkg.Status),
		UploadProgress:   int32(pkg.UploadProgress),
		ErrorMessage:     pkg.ErrorMessage,
		LastDeployTime:   formatTime(pkg.LastDeployTime),
		IsDeleted:        pkg.IsDeleted,
		CreatedTime:      formatTime(pkg.CreateTime),
		UpdatedTime:      formatTime(pkg.ModifyTime),
	}
}

// ========== 时间格式化辅助 ==========

// formatTime 将 time.Time / *time.Time 转为 RFC3339 字符串，零值与 nil 返回空串。
func formatTime(t interface{}) string {
	if t == nil {
		return ""
	}
	switch v := t.(type) {
	case time.Time:
		if v.IsZero() {
			return ""
		}
		return v.Format(time.RFC3339)
	case *time.Time:
		if v == nil || v.IsZero() {
			return ""
		}
		return v.Format(time.RFC3339)
	}
	return ""
}

// parseTime 将 RFC3339 字符串解析为 time.Time，空串或解析失败返回零值。
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// interfaceToStruct 将任意 Go 值转为 google.protobuf.Struct。
func interfaceToStruct(v interface{}) *structpb.Struct {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return nil
		}
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return nil
		}
		if m, ok := parsed.(map[string]interface{}); ok {
			st, err := structpb.NewStruct(m)
			if err == nil {
				return st
			}
		}
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return st
}

// calcNodeStatusText 依据最近心跳与超时阈值计算状态文本。
func calcNodeStatusText(lastHeartbeat *time.Time, timeoutThreshold int) string {
	if lastHeartbeat == nil || lastHeartbeat.IsZero() {
		return "offline"
	}
	threshold := time.Duration(timeoutThreshold) * time.Second
	if threshold <= 0 {
		threshold = 60 * time.Second // 默认 60s
	}
	if time.Since(*lastHeartbeat) > 2*threshold {
		return "offline"
	}
	if time.Since(*lastHeartbeat) > threshold {
		return "timeout"
	}
	return "online"
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
