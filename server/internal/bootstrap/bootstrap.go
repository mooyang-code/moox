package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/config"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize 初始化应用
// 这是应用启动的统一入口，完成所有初始化工作
func Initialize(ctx context.Context) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化应用...")

	// 1. 加载配置
	cfg, err := LoadConfigs(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "加载配置失败: %v", err)
		return nil, err
	}

	// 1.1 设置全局配置（供加密等模块使用）
	config.SetGlobalConfig(cfg.App)
	log.InfoContextf(ctx, "全局配置已设置")

	// 2. 启动后台服务
	services, err := StartBackgroundServices(ctx, cfg)
	if err != nil {
		log.ErrorContextf(ctx, "启动后台服务失败: %v", err)
		return nil, err
	}

	// 3. 创建TRPC服务器
	s := trpc.NewServer()

	// 4. 注册TRPC服务
	if err := RegisterTRPCServices(s, cfg, services); err != nil {
		log.ErrorContextf(ctx, "注册TRPC服务失败: %v", err)
		return nil, err
	}

	log.InfoContextf(ctx, "应用初始化完成")
	return s, nil
}
