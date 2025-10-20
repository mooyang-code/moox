package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/config"
	authcfg "github.com/mooyang-code/moox/server/internal/service/auth/config"
	heartbeatcfg "github.com/mooyang-code/moox/server/internal/service/cloudnode/config"

	"trpc.group/trpc-go/trpc-go/log"
)

// Config 应用配置集合
type Config struct {
	App       *config.AppConfig
	Auth      *authcfg.Config
	Heartbeat *heartbeatcfg.Config
}

// LoadConfigs 加载所有配置
func LoadConfigs(ctx context.Context) (*Config, error) {
	log.Info("正在加载应用配置...")

	// 1. 加载应用配置
	appCfg, err := config.Load("./config/app.yaml")
	if err != nil {
		return nil, err
	}
	log.InfoContextf(ctx, "应用配置加载成功，环境: %s，端口: %d", appCfg.Server.Environment, appCfg.Server.Port)

	// 2. 加载认证配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		return nil, err
	}
	log.Info("认证配置加载成功")

	// 3. 加载心跳服务配置
	heartbeatCfg := heartbeatcfg.LoadConfig()
	log.Info("心跳服务配置加载成功")

	return &Config{
		App:       appCfg,
		Auth:      authCfg,
		Heartbeat: heartbeatCfg,
	}, nil
}
