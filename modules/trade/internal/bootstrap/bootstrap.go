// Package bootstrap 是 moox-trade 进程的启动入口编排：
// 加载配置 → 初始化 SQLite/DAO → 装配 service → 注册 9 个 tRPC service。
package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all" // 注册 binance/okx 适配器
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/exchangebridge"
	kernelstore "github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	"github.com/mooyang-code/moox/modules/trade/internal/service/database"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var tradeStartedAt = time.Now()
var kernelEventBus struct {
	sync.RWMutex
	client *jetstream.Client
}

func setKernelEventBusClient(client *jetstream.Client) {
	kernelEventBus.Lock()
	kernelEventBus.client = client
	kernelEventBus.Unlock()
}
func kernelEventBusReady() bool {
	kernelEventBus.RLock()
	defer kernelEventBus.RUnlock()
	return kernelEventBus.client != nil && kernelEventBus.client.Ready()
}

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
	tradeStore, err := kernelstore.Open(appCfg.Database.Path)
	if err != nil {
		return nil, err
	}
	kernel := &command.Engine{Store: tradeStore, Resolver: exchangebridge.Resolver{Store: store, Factory: exchange.New}}

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
	rpc.RegisterAll(s, svc, kernel)
	if err := startKernelWorkers(ctx, appCfg.EventBus, tradeStore, kernel); err != nil {
		return nil, err
	}
	registerMetricsReporter(s)
	if err := registerHealth(s, appCfg, tradeStore); err != nil {
		return nil, err
	}

	log.InfoContextf(ctx, "moox-trade 初始化完成，交易主链路使用 EventBus，定时轮询同步已停用")
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("trade_account"))
	if err != nil {
		log.Warnf("trade metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.trade.metrics.timer")
	if service == nil {
		log.Warn("trade metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func registerHealth(s *server.Server, cfg *config.AppConfig, store *kernelstore.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("trade", "trade", "", "")
	state.SnapshotFunc = tradeHealthSnapshot(store, state)
	if s == nil {
		return fmt.Errorf("trade health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.trade.Health"), state); err != nil {
		return fmt.Errorf("trade health server failed to start: %w", err)
	}
	return nil
}

func tradeHealthSnapshot(store *kernelstore.Store, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		stats, err := store.Health(ctx)
		busReady := kernelEventBusReady()
		outboxLag := time.Duration(0)
		if !stats.OldestOutbox.IsZero() {
			outboxLag = time.Since(stats.OldestOutbox)
		}
		privateReady := stats.OpenOrders == 0 || telemetry.PrivateStreamsReady()
		ready := err == nil && busReady && outboxLag <= time.Minute && privateReady
		state.SetReady(ready)
		telemetry.UnknownOrders.Set(float64(stats.UnknownOrders))
		telemetry.OutboxLag.Set(outboxLag.Seconds())
		rsp := healthz.Base("trade", "trade", "", "", tradeStartedAt, ready)
		rsp.Details = map[string]any{
			"database_ready":             err == nil,
			"eventbus_ready":             busReady,
			"outbox_pending":             stats.PendingOutbox,
			"outbox_lag_seconds":         outboxLag.Seconds(),
			"unknown_orders":             stats.UnknownOrders,
			"open_orders":                stats.OpenOrders,
			"private_stream_ready":       privateReady,
			"private_stream_connections": telemetry.PrivateConnectedCount(),
		}
		return rsp
	}
}
