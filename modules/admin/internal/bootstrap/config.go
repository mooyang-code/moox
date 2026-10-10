package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/console"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"

	"trpc.group/trpc-go/trpc-go/log"
)

// Config 应用配置集合
type Config struct {
	App         *config.AppConfig
	Auth        *authcfg.Config
	Console     *console.Config
	AdminNodeID string
}

// LoadConfigs 加载系统中各个模块配置
func LoadConfigs(ctx context.Context) (*Config, error) {
	log.Info("正在加载应用配置...")

	// 1. 加载应用配置
	appCfg, err := config.Load("./config/app.yaml")
	if err != nil {
		return nil, err
	}
	if err := loadEncryptionKey(); err != nil {
		return nil, err
	}
	if err := validateSetupListener("./config/trpc_go.yaml"); err != nil {
		return nil, err
	}
	if err := validateGatewayControlListener("./config/trpc_go.yaml"); err != nil {
		return nil, err
	}
	adminNodeID := strings.TrimSpace(os.Getenv("MOOX_ADMIN_NODE_ID"))
	if !servicecatalog.ValidHostID(adminNodeID) {
		return nil, fmt.Errorf("MOOX_ADMIN_NODE_ID must be a canonical control host ID")
	}
	log.Info("应用配置加载成功")

	// 2. 加载认证配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		return nil, err
	}
	log.Info("认证配置加载成功")

	// 3. 加载控制台配置
	consoleCfg, err := console.LoadConfig()
	if err != nil {
		return nil, err
	}
	console.SetConfig(consoleCfg)
	if len(strings.TrimSpace(consoleCfg.JWT.SecretKey)) < 32 {
		return nil, fmt.Errorf("jwt.secret_key must contain at least 32 characters")
	}
	if err := validateConsoleCORS(consoleCfg); err != nil {
		return nil, err
	}
	log.Info("控制台配置加载成功")

	// 5. 创建配置对象
	cfg := &Config{
		App:         appCfg,
		Auth:        authCfg,
		Console:     consoleCfg,
		AdminNodeID: adminNodeID,
	}
	return cfg, nil
}

func validateGatewayControlListener(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read gateway control listener config: %w", err)
	}
	var document struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Address  string `yaml:"address"`
				Nic      string `yaml:"nic"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode gateway control listener config")
	}
	count := 0
	for _, listener := range document.Server.Services {
		if listener.Name != servicecatalog.GatewayControlPath {
			continue
		}
		count++
		ip := net.ParseIP(listener.IP)
		if ip == nil || !ip.IsLoopback() || listener.Port != 11112 || listener.Network != "tcp" || listener.Protocol != "trpc" || listener.Address != "" || listener.Nic != "" {
			return fmt.Errorf("gateway control listener must bind tcp trpc to loopback port 11112 without address or NIC override")
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one gateway control listener is required")
	}
	return nil
}

func validateSetupListener(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read setup listener config: %w", err)
	}
	var document struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode setup listener config")
	}
	for _, service := range document.Server.Services {
		if service.Name != "trpc.moox.admin.Setup" {
			continue
		}
		address := net.ParseIP(strings.TrimSpace(service.IP))
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("setup listener must bind to loopback")
		}
		if service.Port != 11110 || service.Network != "tcp" || service.Protocol != "trpc" {
			return fmt.Errorf("setup listener must use tcp trpc on port 11110")
		}
		return nil
	}
	return fmt.Errorf("setup listener trpc.moox.admin.Setup is required")
}

func loadEncryptionKey() error {
	if strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY")) != "" {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE"))
	if path == "" {
		return fmt.Errorf("MOOX_ADMIN_ENCRYPTION_KEY_FILE is required in server mode")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat admin encryption key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("admin encryption key file must be a regular 0600 file")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		if err == nil {
			err = fmt.Errorf("file is empty")
		}
		return fmt.Errorf("read admin encryption key file: %w", err)
	}
	return os.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", strings.TrimSpace(string(raw)))
}

func validateConsoleCORS(cfg *console.Config) error {
	if cfg == nil {
		return fmt.Errorf("console config is nil")
	}
	if len(cfg.CORS.AllowedOrigins) == 0 {
		if cfg.Console.Debug {
			log.Warn("cors.allowed_origins 未配置，非 debug 环境将无法设置 CORS 响应头")
			return nil
		}
		return fmt.Errorf("cors.allowed_origins must not be empty when console.debug is false")
	}
	if !cfg.Console.Debug && consoleContainsWildcardOrigin(cfg.CORS.AllowedOrigins) {
		log.Warn("生产环境 cors.allowed_origins 包含 '*'，建议改为具体前端域名")
	}
	return nil
}

func consoleContainsWildcardOrigin(origins []string) bool {
	for _, origin := range origins {
		if strings.TrimSpace(origin) == "*" {
			return true
		}
	}
	return false
}
