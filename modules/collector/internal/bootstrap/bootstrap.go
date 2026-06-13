package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go/log"
)

// Bootstrap 系统启动器
type Bootstrap struct {
	config    *config.AppConfig
	services  *Services
	startTime time.Time // 启动时间
}

// New 创建新的启动器
func New(cfg *config.AppConfig) *Bootstrap {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	return &Bootstrap{
		config:    cfg,
		startTime: time.Now(),
	}
}

// Initialize 初始化启动器
func (b *Bootstrap) Initialize(ctx context.Context) error {
	log.Info("Bootstrap 开始初始化...")

	// 1. 加载配置
	cfg, err := config.LoadConfigs(b.config)
	if err != nil {
		log.Errorf("加载配置失败: %v", err)
		return err
	}
	b.config = cfg

	// 2. 启动后台服务
	services, err := StartBackgroundServices(ctx)
	if err != nil {
		log.Errorf("启动后台服务失败: %v", err)
		return err
	}

	// 3. 保存服务实例
	b.services = services

	// 注册TRPC服务（使用trpc的定时器）
	if err := RegisterTRPCServices(); err != nil {
		log.Errorf("注册TRPC服务失败: %v", err)
		return err
	}
	log.Infof("Bootstrap 初始化完成, 耗时: %dms", time.Since(b.startTime).Milliseconds())
	return nil
}

// GetConfig 获取配置实例
func (b *Bootstrap) GetConfig() *config.AppConfig {
	return b.config
}

// GetServices 获取服务实例
func (b *Bootstrap) GetServices() *Services {
	return b.services
}
