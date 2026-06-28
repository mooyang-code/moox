// Package infraconfig 提供 moox 基础设施配置的唯一读取入口。
//
// 配置来源：infra/infra.yaml（base，入库占位）+ infra/infra.local.yaml（overlay，
// gitignored，真实部署值）。所有服务运行时、部署脚本、CLI、测试、文档均通过本包
// 读取 IP/端口，仓库内不再出现写死的真实 IP。
//
// 路径解析顺序：
//  1. 环境变量 MOOX_INFRA_CONFIG 指向 infra.yaml 文件或其所在目录；
//  2. 从当前工作目录向上回溯，定位 infra/infra.yaml；
//  3. 失败则返回错误。
package infraconfig

import (
	"fmt"
	"os"
	"path/filepath"
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

// Config 是 infra 配置的顶层结构。
type Config struct {
	Services struct {
		StorageAccess ServiceEndpoint `yaml:"storage_access"`
		XData         ServiceEndpoint `yaml:"xdata"`
		AdminGateway  ServiceEndpoint `yaml:"admin_gateway"`
		WebHost       ServiceEndpoint `yaml:"web_host"`
		Trade         ServiceEndpoint `yaml:"trade"`
	} `yaml:"services"`
	Remote struct {
		Host string `yaml:"host"`
		SSH  string `yaml:"ssh"`
	} `yaml:"remote"`
}

var (
	once   sync.Once
	loaded *Config
	loadErr error
)

// Load 加载并缓存配置（base 合并 overlay）。多次调用返回同一结果。
func Load() (*Config, error) {
	once.Do(func() { loaded, loadErr = load() })
	return loaded, loadErr
}

// Reset 重置缓存，仅供测试在切换 infra 路径后重新加载。
func Reset() {
	once = sync.Once{}
	loaded = nil
	loadErr = nil
}

// MustLoad 同 Load，出错时 panic。
func MustLoad() *Config {
	c, err := Load()
	if err != nil {
		panic(fmt.Errorf("infraconfig: %w", err))
	}
	return c
}

func load() (*Config, error) {
	basePath, err := resolveInfraPath()
	if err != nil {
		return nil, err
	}
	cfg, err := readYAML(basePath)
	if err != nil {
		return nil, fmt.Errorf("load base %s: %w", basePath, err)
	}
	localPath := filepath.Join(filepath.Dir(basePath), "infra.local.yaml")
	if _, statErr := os.Stat(localPath); statErr == nil {
		overlay, err := readYAML(localPath)
		if err != nil {
			return nil, fmt.Errorf("load local %s: %w", localPath, err)
		}
		merge(cfg, overlay)
	}
	return cfg, nil
}

// resolveInfraPath 解析 infra.yaml 的绝对路径。
func resolveInfraPath() (string, error) {
	if v := os.Getenv("MOOX_INFRA_CONFIG"); v != "" {
		fi, err := os.Stat(v)
		if err != nil {
			return "", fmt.Errorf("MOOX_INFRA_CONFIG=%s 不可访问: %w", v, err)
		}
		if fi.IsDir() {
			return filepath.Join(v, "infra.yaml"), nil
		}
		return v, nil
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
	return "", fmt.Errorf("未找到 infra/infra.yaml：请设置 MOOX_INFRA_CONFIG 指向 infra 目录或文件（当前工作目录=%s）", cwd)
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

// merge 用 overlay 覆盖 base 的非零值字段。
func merge(base, overlay *Config) {
	mergeEndpoint(&base.Services.StorageAccess, overlay.Services.StorageAccess)
	mergeEndpoint(&base.Services.XData, overlay.Services.XData)
	mergeEndpoint(&base.Services.AdminGateway, overlay.Services.AdminGateway)
	mergeEndpoint(&base.Services.WebHost, overlay.Services.WebHost)
	mergeEndpoint(&base.Services.Trade, overlay.Services.Trade)
	if overlay.Remote.Host != "" {
		base.Remote.Host = overlay.Remote.Host
	}
	if overlay.Remote.SSH != "" {
		base.Remote.SSH = overlay.Remote.SSH
	}
}

func mergeEndpoint(dst *ServiceEndpoint, src ServiceEndpoint) {
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
}

// ============================================================================
// 访问器：供消费方按需取值，避免直接暴露内部结构。
// ============================================================================

// StorageAccessURL 返回 storage access 服务 URL。
func StorageAccessURL() string { return MustLoad().Services.StorageAccess.URL() }

// XDataURL 返回 xData 服务 URL。
func XDataURL() string { return MustLoad().Services.XData.URL() }

// AdminGatewayHost 返回管理台网关 host。
func AdminGatewayHost() string { return MustLoad().Services.AdminGateway.Host }

// AdminGatewayPort 返回管理台网关端口。
func AdminGatewayPort() int { return MustLoad().Services.AdminGateway.Port }

// WebHostPort 返回 web-host 端口。
func WebHostPort() int { return MustLoad().Services.WebHost.Port }

// TradeHost 返回 trade 服务 host。
func TradeHost() string { return MustLoad().Services.Trade.Host }

// TradePort 返回 trade 服务端口。
func TradePort() int { return MustLoad().Services.Trade.Port }

// RemoteHost 返回部署目标主机。
func RemoteHost() string { return MustLoad().Remote.Host }

// RemoteSSH 返回部署目标 SSH 目标（user@host）。
func RemoteSSH() string { return MustLoad().Remote.SSH }
