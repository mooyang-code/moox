// Package infraconfig 提供 MooX 基础设施配置的唯一读取入口。
//
// 配置来源（按优先级合并）：
//  1. infra/infra.local.yaml  真实部署值（gitignored，可选）
//  2. infra/infra.yaml        占位默认值（入库）
//
// 配置目录解析顺序：
//  1. 环境变量 MOOX_INFRA_CONFIG 指向的 infra.yaml 文件
//  2. 从当前工作目录向上回溯，定位仓库根的 infra/infra.yaml
//
// 所有运行时服务、部署脚本、CLI、测试、文档统一通过本包读取 IP/端口，
// 仓库其它位置不得硬编码真实 IP。
package infraconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ServiceEndpoint 描述一个服务的 host:port 端点。
type ServiceEndpoint struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// URL 返回 http://host:port 形式的端点 URL。
func (e ServiceEndpoint) URL() string {
	return fmt.Sprintf("http://%s:%d", e.Host, e.Port)
}

// HostPort 返回 host:port 字符串。
func (e ServiceEndpoint) HostPort() string {
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// Config 是完整的基础设施配置。
type Config struct {
	Services struct {
		StorageAccess ServiceEndpoint `yaml:"storage_access"`
		XData         ServiceEndpoint `yaml:"xdata"`
		AdminGateway  ServiceEndpoint `yaml:"admin_gateway"`
		WebHost       ServiceEndpoint `yaml:"web_host"`
		Trade         ServiceEndpoint `yaml:"trade"`
		Collector     ServiceEndpoint `yaml:"collector"`
	} `yaml:"services"`
	Remote struct {
		Host string `yaml:"host"`
		SSH  string `yaml:"ssh"`
	} `yaml:"remote"`

	basePath string `yaml:"-"`
}

var (
	mu       sync.Mutex
	cached   *Config
	loadErr  error
)

// Load 加载并缓存配置（合并 base + local）。重复调用返回同一实例。
func Load() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil || loadErr != nil {
		return cached, loadErr
	}
	cached, loadErr = loadOnce()
	return cached, loadErr
}

// MustLoad 加载配置，失败时 panic。供命令行工具/main 使用。
func MustLoad() *Config {
	cfg, err := Load()
	if err != nil {
		panic(fmt.Sprintf("infraconfig load: %v", err))
	}
	return cfg
}

// Reset 重置缓存。仅测试使用。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	cached, loadErr = nil, nil
}

func loadOnce() (*Config, error) {
	basePath, err := resolveBasePath()
	if err != nil {
		return nil, err
	}
	cfg, err := readYAML(basePath)
	if err != nil {
		return nil, fmt.Errorf("read base %s: %w", basePath, err)
	}
	cfg.basePath = basePath

	localPath := filepath.Join(filepath.Dir(basePath), "infra.local.yaml")
	if _, statErr := os.Stat(localPath); statErr == nil {
		local, err := readYAML(localPath)
		if err != nil {
			return nil, fmt.Errorf("read local %s: %w", localPath, err)
		}
		merge(cfg, local)
	}
	return cfg, nil
}

// resolveBasePath 解析 infra.yaml 的绝对路径。
func resolveBasePath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MOOX_INFRA_CONFIG")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("MOOX_INFRA_CONFIG=%s: %w", p, err)
		}
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "infra", "infra.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("infra/infra.yaml 未找到（cwd=%s，可用 MOOX_INFRA_CONFIG 指定）", cwd)
}

func readYAML(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// merge 将 local 的非零值覆盖到 base（深度合并）。
func merge(base, local *Config) {
	mergeEndpoint(&base.Services.StorageAccess, local.Services.StorageAccess)
	mergeEndpoint(&base.Services.XData, local.Services.XData)
	mergeEndpoint(&base.Services.AdminGateway, local.Services.AdminGateway)
	mergeEndpoint(&base.Services.WebHost, local.Services.WebHost)
	mergeEndpoint(&base.Services.Trade, local.Services.Trade)
	mergeEndpoint(&base.Services.Collector, local.Services.Collector)
	if local.Remote.Host != "" {
		base.Remote.Host = local.Remote.Host
	}
	if local.Remote.SSH != "" {
		base.Remote.SSH = local.Remote.SSH
	}
}

func mergeEndpoint(base *ServiceEndpoint, local ServiceEndpoint) {
	if local.Host != "" {
		base.Host = local.Host
	}
	if local.Port != 0 {
		base.Port = local.Port
	}
}

// ===== 访问器 =====

// StorageAccessURL 返回 storage access 服务 URL。
func StorageAccessURL() string { return MustLoad().Services.StorageAccess.URL() }

// XDataURL 返回 xData 服务 URL。
func XDataURL() string { return MustLoad().Services.XData.URL() }

// AdminGateway 返回 admin 网关端点。
func AdminGateway() ServiceEndpoint { return MustLoad().Services.AdminGateway }

// WebHost 返回 web-host 端点。
func WebHost() ServiceEndpoint { return MustLoad().Services.WebHost }

// Trade 返回 trade 服务端点。
func Trade() ServiceEndpoint { return MustLoad().Services.Trade }

// RemoteHost 返回部署目标机 IP。
func RemoteHost() string { return MustLoad().Remote.Host }

// RemoteSSH 返回部署 SSH 目标。
func RemoteSSH() string { return MustLoad().Remote.SSH }

// BasePath 返回已加载的 infra.yaml 绝对路径（供调试/测试）。
func BasePath() string {
	cfg, err := Load()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.basePath
}

// Atoi 容错整数解析（供 shell 工具复用）。
func Atoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
