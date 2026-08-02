package runtime

import (
	"log"
	"os"
	"strings"
	"sync"
)

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

func GetStorageRPCGatewayTarget() string {
	if envTarget := strings.TrimSpace(os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET")); IsStorageTRPCTarget(envTarget) {
		return trimTarget(envTarget)
	}
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	localTarget := ""
	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		localTarget = strings.TrimSpace(LocalAppConfig.System.StorageRPC.GatewayTarget)
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
