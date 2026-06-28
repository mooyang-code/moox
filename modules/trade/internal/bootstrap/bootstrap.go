// Package bootstrap 是 moox-trade 进程的启动入口编排：
// 加载配置 → 初始化 SQLite/DAO → 装配 service → 注册 9 个 tRPC service。
package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all" // 注册 binance/okx 适配器
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	"github.com/mooyang-code/moox/modules/trade/internal/service/database"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize 初始化 moox-trade 进程：配置 + 持久化 + 服务注册。
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-trade...")

	// 1. 加载应用配置（trpc_go.yaml 由 trpc-go 运行时自动加载）
	appCfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载应用配置失败: %v", err)
		return nil, err
	}
	config.SetGlobalConfig(appCfg)
	log.InfoContextf(ctx, "应用配置加载成功: db=%s", appCfg.Database.Path)

	// 2. 初始化数据库（建表）
	dm := database.NewManager()
	if err := dm.Initialize(&appCfg.Database); err != nil {
		log.ErrorContextf(ctx, "初始化数据库失败: %v", err)
		return nil, err
	}
	store := dao.New(dm.GetDB(), appCfg.Security.EncryptionKey)

	// 3. 装配领域服务
	svc := service.New("trade", service.WithStore(store))

	// 4. 注册 9 个 tRPC service
	rpc.RegisterAll(s, svc)

	log.InfoContextf(ctx, "moox-trade 初始化完成，已注册 9 个 service")
	return s, nil
}
