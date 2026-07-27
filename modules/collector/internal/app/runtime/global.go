package runtime

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
)

// Config 全局配置结构
type Config struct {
	ServiceGatewayTarget    string
	NodeInfo                NodeInfoConfig
	StorageRPCGatewayTarget string
	// 后续扩展其他配置项
	// Database DatabaseConfig
	// Cache CacheConfig
	// Metrics MetricsConfig
}

// NodeInfoConfig 节点信息配置
type NodeInfoConfig struct {
	NodeID  string
	Version string
}

// GlobalConfig 全局配置实例
var GlobalConfig Config

var configMu sync.RWMutex

type readiness struct {
	once sync.Once
	done chan struct{}
}

func newReadiness() *readiness {
	return &readiness{done: make(chan struct{})}
}

func (r *readiness) signal() {
	r.once.Do(func() {
		close(r.done)
	})
}

func (r *readiness) wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var processReadiness = newReadiness()

// SignalReadinessIfConfigured marks the SCF runtime ready after a keepalive
// has supplied both the node identity and service gateway.
func SignalReadinessIfConfigured() bool {
	configMu.RLock()
	ready := strings.TrimSpace(GlobalConfig.NodeInfo.NodeID) != "" &&
		strings.TrimSpace(GlobalConfig.ServiceGatewayTarget) != ""
	configMu.RUnlock()
	if !ready {
		return false
	}
	processReadiness.signal()
	return true
}

// WaitForReadiness waits until the first complete keepalive initializes the runtime.
func WaitForReadiness(ctx context.Context) error {
	return processReadiness.wait(ctx)
}

// UpdateServiceGatewayTarget updates the /api/service gateway target used by SCF callbacks.
func UpdateServiceGatewayTarget(target string) {
	configMu.Lock()
	defer configMu.Unlock()
	updateServiceGatewayTargetLocked(target)
}

// GetServiceGatewayTarget returns the /api/service gateway target.
func GetServiceGatewayTarget() string {
	configMu.RLock()
	defer configMu.RUnlock()
	if GlobalConfig.ServiceGatewayTarget != "" {
		return GlobalConfig.ServiceGatewayTarget
	}
	return ""
}

func updateServiceGatewayTargetLocked(target string) {
	target = normalizeServiceGatewayTarget(target)
	if target == "" {
		return
	}
	GlobalConfig.ServiceGatewayTarget = target
}

func normalizeServiceGatewayTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + raw
}

// UpdateStorageRPCGatewayTarget updates direct tRPC targets for storage metadata/access.
func UpdateStorageRPCGatewayTarget(target string) {
	configMu.Lock()
	defer configMu.Unlock()
	if IsStorageTRPCTarget(target) {
		GlobalConfig.StorageRPCGatewayTarget = trimTarget(target)
	}
}

func getRuntimeStorageTargets() (string, string) {
	configMu.RLock()
	defer configMu.RUnlock()
	return GlobalConfig.StorageRPCGatewayTarget, GlobalConfig.StorageRPCGatewayTarget
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

func GetStorageRPCGatewayTarget() string {
	runtimeTarget, _ := getRuntimeStorageTargets()
	if runtimeTarget != "" {
		return runtimeTarget
	}
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
