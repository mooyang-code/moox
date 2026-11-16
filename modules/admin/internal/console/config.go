package console

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v2"
)

// 全局配置供控制台的来源校验和登录限流使用。
var (
	consoleConfig *Config
	configMutex   sync.RWMutex
)

// Config 控制台服务配置
type Config struct {
	Console   ConsoleConfig   `yaml:"console"`    // 控制台配置
	RateLimit RateLimitConfig `yaml:"rate_limit"` // 限流配置
}

// ConsoleConfig 控制台配置
type ConsoleConfig struct {
	// AllowedOrigins 是本站之外还接受的 Origin（例如 http://localhost:9527），只给本机开发经开发服务器代理时用，生产留空。
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// RateLimitConfig 限流配置：只对列出的接口限流（登录等免会话或敏感接口），没列出的不限。
type RateLimitConfig struct {
	MethodLimits map[string]MethodLimit `yaml:"method_limits"`
}

// MethodLimit 接口级别限流配置
type MethodLimit struct {
	QPS   int `yaml:"qps"`
	Burst int `yaml:"burst"`
}

// SetConfig 设置控制台配置
func SetConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	consoleConfig = cfg
}

// GetConfig 获取控制台配置
func GetConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return consoleConfig
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	yamlFile, err := os.ReadFile("./config/console.yaml")
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %+v", err)
	}
	var config Config
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %+v", err)
	}
	for _, origin := range config.Console.AllowedOrigins {
		if !validOrigin(origin) {
			return nil, fmt.Errorf("console.allowed_origins 里的 %q 不是合法的来源，要写成 scheme://主机[:端口]，不能用 *", origin)
		}
	}
	return &config, nil
}

// validOrigin 判断是不是浏览器 Origin 头的形式：只有协议、主机和可选端口。
func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.Contains(u.Host, "*") &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
