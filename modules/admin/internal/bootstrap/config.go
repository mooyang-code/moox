package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	cloudnodecfg "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"

	"trpc.group/trpc-go/trpc-go/log"
)

// Config 应用配置集合
type Config struct {
	App       *config.AppConfig
	Auth      *authcfg.Config
	CloudNode *cloudnodecfg.Config
	Gateway   *gateway.Config
	Service   *config.ServiceConfig
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
	log.Info("应用配置加载成功")

	// 2. 加载认证配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		return nil, err
	}
	log.Info("认证配置加载成功")

	// 3. 加载云节点服务配置
	cloudNodeCfg := cloudnodecfg.LoadConfig()
	log.Info("云节点服务配置加载成功")

	// 4. 加载网关配置
	gatewayCfg, err := gateway.LoadConfig()
	if err != nil {
		return nil, err
	}
	gateway.SetConfig(gatewayCfg)
	if strings.TrimSpace(gatewayCfg.JWT.SecretKey) == "" {
		return nil, fmt.Errorf("jwt.secret_key must not be empty")
	}
	log.Info("网关配置加载成功")

	// 5. 加载并注入DNSProxy配置
	// DNSProxy配置加载并直接注入，不保存在Config结构中
	dnsProxyCfg, err := dnsproxy.LoadConfig()
	if err != nil {
		return nil, err
	}
	dnsproxy.SetConfig(dnsProxyCfg)
	log.Info("DNSProxy配置加载成功")

	// 6. 加载服务配置（从trpc配置中获取服务端口等信息）
	serviceCfg, err := config.LoadServiceConfig()
	if err != nil {
		log.Warnf("服务配置加载失败: %v", err)
		// 服务配置加载失败不阻断应用启动
		serviceCfg = &config.ServiceConfig{}
	}
	config.SetGlobalServiceConfig(serviceCfg)
	log.Info("服务配置加载成功")

	// 7. 创建配置对象
	cfg := &Config{
		App:       appCfg,
		Auth:      authCfg,
		CloudNode: cloudNodeCfg,
		Gateway:   gatewayCfg,
		Service:   serviceCfg,
	}
	return cfg, nil
}
