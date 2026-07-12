package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"

	"trpc.group/trpc-go/trpc-go/log"
)

// Config 应用配置集合
type Config struct {
	App     *config.AppConfig
	Auth    *authcfg.Config
	Gateway *gateway.Config
}

// LoadConfigs 加载系统中各个模块配置
func LoadConfigs(ctx context.Context) (*Config, error) {
	log.Info("正在加载应用配置...")

	// 1. 加载应用配置
	appCfg, err := config.Load("./config/app.yaml")
	if err != nil {
		return nil, err
	}
	config.SetGlobalConfig(appCfg) // 设置全局配置，供其他模块使用
	if err := loadEncryptionKey(); err != nil {
		return nil, err
	}
	log.Info("应用配置加载成功")

	// 2. 加载认证配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		return nil, err
	}
	log.Info("认证配置加载成功")

	// 3. 加载网关配置
	gatewayCfg, err := gateway.LoadConfig()
	if err != nil {
		return nil, err
	}
	gateway.SetConfig(gatewayCfg)
	if len(strings.TrimSpace(gatewayCfg.JWT.SecretKey)) < 32 {
		return nil, fmt.Errorf("jwt.secret_key must contain at least 32 characters")
	}
	if err := validateGatewayCORS(gatewayCfg); err != nil {
		return nil, err
	}
	log.Info("网关配置加载成功")

	// 4. 加载并注入DNSProxy配置
	// DNSProxy配置加载并直接注入，不保存在Config结构中
	dnsProxyCfg, err := dnsproxy.LoadConfig()
	if err != nil {
		return nil, err
	}
	dnsproxy.SetConfig(dnsProxyCfg)
	log.Info("DNSProxy配置加载成功")

	// 5. 创建配置对象
	cfg := &Config{
		App:     appCfg,
		Auth:    authCfg,
		Gateway: gatewayCfg,
	}
	return cfg, nil
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

func validateGatewayCORS(cfg *gateway.Config) error {
	if cfg == nil {
		return fmt.Errorf("gateway config is nil")
	}
	if len(cfg.CORS.AllowedOrigins) == 0 {
		if cfg.Gateway.Debug {
			log.Warn("cors.allowed_origins 未配置，非 debug 环境将无法设置 CORS 响应头")
			return nil
		}
		return fmt.Errorf("cors.allowed_origins must not be empty when gateway.debug is false")
	}
	if !cfg.Gateway.Debug && gatewayContainsWildcardOrigin(cfg.CORS.AllowedOrigins) {
		log.Warn("生产环境 cors.allowed_origins 包含 '*'，建议改为具体前端域名")
	}
	return nil
}

func gatewayContainsWildcardOrigin(origins []string) bool {
	for _, origin := range origins {
		if strings.TrimSpace(origin) == "*" {
			return true
		}
	}
	return false
}
