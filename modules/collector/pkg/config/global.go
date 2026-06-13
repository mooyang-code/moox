package config

import (
	"log"
	"sync"
)

const (
	// MooxServerServiceName 服务名称常量
	MooxServerServiceName = "moox-server"
)

// Config 全局配置结构
type Config struct {
	Server   ServerConfig
	NodeInfo NodeInfoConfig
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
	defer configMu.RUnlock()

	nodeID := GlobalConfig.NodeInfo.NodeID

	// 确保本地配置已初始化
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	defer localAppConfigMu.RUnlock()

	version := "unknown"
	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		version = LocalAppConfig.System.Version
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

// GetStorageURL 获取存储服务地址
func GetStorageURL() string {
	// 确保本地配置已初始化
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	defer localAppConfigMu.RUnlock()

	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		return LocalAppConfig.System.StorageURL
	}
	return ""
}
