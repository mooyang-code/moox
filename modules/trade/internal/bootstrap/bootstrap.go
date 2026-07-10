// Package bootstrap 是 moox-trade 进程的启动入口编排：
// 加载配置 → 初始化 SQLite/DAO → 装配 service → 注册 9 个 tRPC service。
package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/metricspublish"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all" // 注册 binance/okx 适配器
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	"github.com/mooyang-code/moox/modules/trade/internal/service/database"
	"github.com/mooyang-code/moox/packages/healthz"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var tradeStartedAt = time.Now()

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
	secretSource := secretclient.New(secretclient.Config{
		GatewayBaseURL: appCfg.ControlGateway.BaseURL,
		ServiceAuth: secretclient.ServiceAuthConfig{
			Version:    appCfg.ControlGateway.ServiceAuth.Version,
			AccessKey:  appCfg.ControlGateway.ServiceAuth.AccessKey,
			SecretKey:  appCfg.ControlGateway.ServiceAuth.SecretKey,
			ExpireSecs: appCfg.ControlGateway.ServiceAuth.ExpireSeconds,
		},
	})
	svc := service.New("trade", service.WithStore(store), service.WithExchangeSecretSource(secretSource))

	// 4. 注册 9 个 tRPC service
	rpc.RegisterAll(s, svc)
	rpc.SetDefaultSyncService(svc)
	registerTradeSyncSchedule(s)
	registerMetricsReporter(s)
	startHealthServer(ctx, appCfg)

	log.InfoContextf(ctx, "moox-trade 初始化完成，已注册 9 个 RPC service 和定时同步 service")
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil { return }
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("moox-trade"))
	if err != nil { log.Warnf("trade metrics reporter disabled: %v", err); return }
	service := s.Service("trpc.moox.trade.metrics.timer")
	if service == nil { log.Warn("trade metrics timer service is not configured, skip register"); return }
	timer.RegisterHandlerService(service, h.Handle)
}

func startHealthServer(ctx context.Context, cfg *config.AppConfig) {
	if cfg == nil {
		return
	}
	if _, err := healthz.Start(ctx, cfg.Health.Addr, tradeHealthSnapshot(cfg)); err != nil {
		log.ErrorContextf(ctx, "trade health server failed to start: %v", err)
	}
}

func tradeHealthSnapshot(cfg *config.AppConfig) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := healthz.Base("trade", "trade", "", "", tradeStartedAt, true)
		rsp.Details = map[string]any{
			"database":        "ok",
			"sync_enabled":    cfg.Sync.Enabled,
			"control_gateway": cfg.ControlGateway.BaseURL,
			"orders_sync":     cfg.Sync.SyncOrders,
			"positions_sync":  cfg.Sync.SyncPositions,
			"max_symbols_run": cfg.Sync.MaxSymbolsPerRun,
		}
		return rsp
	}
}

func registerTradeSyncSchedule(s *server.Server) {
	timer.RegisterScheduler("tradeSyncSchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.trade.sync.timer")
	if service == nil {
		log.Warn("trade sync timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, rpc.HandleSyncSchedule)
}
