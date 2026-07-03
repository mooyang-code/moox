package runtime

import (
	"log"
	"strings"
	"sync"
)

// Config 全局配置结构
type Config struct {
	Server                ServerConfig
	NodeInfo              NodeInfoConfig
	StorageMetadataTarget string
	StorageAccessTarget   string
	// 后续扩展其他配置项
	// Database DatabaseConfig
	// Cache CacheConfig
	// Metrics MetricsConfig
}

// ServerConfig 服务端配置
type ServerConfig struct {
	IP   string
	Port int
}

// NodeInfoConfig 节点信息配置
type NodeInfoConfig struct {
	NodeID  string
	Version string
}

// GlobalConfig 全局配置实例
var GlobalConfig Config

var configMu sync.RWMutex

// UpdateServerInfo 更新服务端配置
func UpdateServerInfo(ip string, port int) {
	configMu.Lock()
	defer configMu.Unlock()
	if ip != "" {
		GlobalConfig.Server.IP = ip
	}
	if port > 0 {
		GlobalConfig.Server.Port = port
	}
}

// GetServerInfo 获取服务端配置副本
func GetServerInfo() (string, int) {
	configMu.RLock()
	defer configMu.RUnlock()
	return GlobalConfig.Server.IP, GlobalConfig.Server.Port
}

// UpdateStorageTargets updates direct tRPC targets for storage metadata/access.
func UpdateStorageTargets(metadataTarget string, accessTarget string) {
	configMu.Lock()
	defer configMu.Unlock()
	if IsStorageTRPCTarget(metadataTarget) {
		GlobalConfig.StorageMetadataTarget = trimTarget(metadataTarget)
	}
	if IsStorageTRPCTarget(accessTarget) {
		GlobalConfig.StorageAccessTarget = trimTarget(accessTarget)
	}
}

func getRuntimeStorageTargets() (string, string) {
	configMu.RLock()
	defer configMu.RUnlock()
	return GlobalConfig.StorageMetadataTarget, GlobalConfig.StorageAccessTarget
}

// UpdateNodeInfo 更新节点信息配置
func UpdateNodeInfo(nodeID string, version string) {
	configMu.Lock()
	defer configMu.Unlock()
	GlobalConfig.NodeInfo.NodeID = nodeID
	GlobalConfig.NodeInfo.Version = version
}

// GetNodeInfo 获取节点信息配置副本
func GetNodeInfo() (string, string) {
	configMu.RLock()
	nodeID := GlobalConfig.NodeInfo.NodeID
	runtimeVersion := GlobalConfig.NodeInfo.Version
	configMu.RUnlock()

	// 确保本地配置已初始化
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	defer localAppConfigMu.RUnlock()

	version := runtimeVersion
	if version == "" {
		version = "unknown"
		if LocalAppConfig != nil && LocalAppConfig.System != nil && LocalAppConfig.System.Version != "" {
			version = LocalAppConfig.System.Version
		}
	}
	return nodeID, version
}

// LocalAppConfig 本地应用配置单例
var (
	LocalAppConfig     *AppConfig
	localAppConfigOnce sync.Once
	localAppConfigMu   sync.RWMutex
)

// InitLocalAppConfig 初始化本地应用配置单例
func InitLocalAppConfig() {
	localAppConfigOnce.Do(func() {
		localAppConfigMu.Lock()
		defer localAppConfigMu.Unlock()

		if LocalAppConfig == nil {
			// 加载本地配置
			cfg := DefaultConfig()
			loadedCfg, err := LoadConfigs(cfg)
			if err != nil {
				log.Printf("Failed to load local config: %v, using default", err)
				LocalAppConfig = cfg
			} else {
				LocalAppConfig = loadedCfg
			}
			log.Printf("Local app config initialized with version: %s", LocalAppConfig.System.Version)
		}
	})
}

// GetStorageMetadataTarget returns the metadata tRPC target.
// SCF/远程采集器优先使用控制面 keepalive 下发的 storage_metadata_trpc 部署。
func GetStorageMetadataTarget() string {
	runtimeMetadata, _ := getRuntimeStorageTargets()
	if runtimeMetadata != "" {
		return runtimeMetadata
	}
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	localTarget := ""
	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		localTarget = strings.TrimSpace(LocalAppConfig.System.StorageMetadataTarget)
	}
	localAppConfigMu.RUnlock()

	if !IsStorageTRPCTarget(localTarget) {
		return ""
	}
	return trimTarget(localTarget)
}

// GetStorageAccessTarget returns the access tRPC target.
// Deprecated system.storage_url is intentionally not consumed by the storage tRPC proxy path.
func GetStorageAccessTarget() string {
	_, runtimeAccess := getRuntimeStorageTargets()
	if runtimeAccess != "" {
		return runtimeAccess
	}
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	localTarget := ""
	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		localTarget = strings.TrimSpace(LocalAppConfig.System.StorageAccessTarget)
	}
	localAppConfigMu.RUnlock()

	if !IsStorageTRPCTarget(localTarget) {
		return ""
	}
	return trimTarget(localTarget)
}

// IsStorageTRPCTarget reports whether a target can be passed to a generated tRPC ClientProxy.
func IsStorageTRPCTarget(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return false
	}
	return !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")
}

func trimTarget(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
