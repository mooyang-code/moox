package bootstrap

import (
	"context"

	_ "github.com/mooyang-code/moox/modules/collector/internal/collector/binance" // 注册 binance 采集器
	"github.com/mooyang-code/moox/modules/collector/internal/dnsproxy"
	"trpc.group/trpc-go/trpc-go/log"
)

// Services 应用服务集合
type Services struct{}

// StartBackgroundServices 启动所有后台服务
func StartBackgroundServices(ctx context.Context) (*Services, error) {
	log.Info("正在启动后台服务...")

	// 1. 初始化任务实例内存存储（已在config包的init中自动初始化）
	log.Info("任务实例存储已初始化，将通过心跳回包自动更新")

	// 2. 初始化 DNS 代理
	if err := initDNSProxy(); err != nil {
		log.Errorf("初始化 DNS 代理失败: %v", err)
		return nil, err
	}

	log.Info("后台服务启动完成")
	return &Services{}, nil
}

// initDNSProxy 初始化 DNS 代理
func initDNSProxy() error {
	log.Info("正在初始化 DNS 代理...")
	dnsproxy.Init()
	log.Info("DNS 代理初始化完成")
	return nil
}
