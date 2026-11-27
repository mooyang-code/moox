package console

import (
	"sync"

	appconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
)

// 全局配置供控制台的来源校验和登录限流使用。
var (
	consoleConfig *Config
	configMutex   sync.RWMutex
)

// Config 控制台服务配置（由 app.yaml 单次加载后注入）。
type Config struct {
	Console   ConsoleConfig
	RateLimit RateLimitConfig
}

// 复用 internal/config 中的配置类型定义，消除双份结构与重复解析。
type (
	ConsoleConfig   = appconfig.ConsoleConfig
	RateLimitConfig = appconfig.RateLimitConfig
	MethodLimit     = appconfig.MethodLimit
)

// SetConfig 设置控制台配置。
func SetConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	consoleConfig = cfg
}

// GetConfig 获取控制台配置。
func GetConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return consoleConfig
}
