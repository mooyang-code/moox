// Package bootstrap 装配 moox-callback-dispatcher 进程。
package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action/delist"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/config"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/health"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/rpc"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/tradegateway"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	moduleName = "callbackdispatcher"
	// 关注项每 30 秒按库里的状态重新计算一次，与指标上报间隔一致。
	attentionInterval = 30 * time.Second
	// EventBus 连不上时每分钟重试一次，不依赖 EventBus 先启动。
	reconnectInterval = time.Minute
)

// Runtime 持有进程资源并决定关闭顺序：先停后台循环，再关库。
type Runtime struct {
	Store     *store.Store
	Gateway   *gatewayclient.Client
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Close 停止后台循环并关闭网关客户端和数据库。
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.workers.Wait()
		if r.Gateway != nil {
			r.closeErr = r.Gateway.Close()
		}
		if r.Store != nil {
			if err := r.Store.Close(); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
	})
	return r.closeErr
}

func (r *Runtime) start(ctx context.Context, name string, run func(context.Context) error) {
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		if err := run(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContextf(ctx, "%s 异常退出: %v", name, err)
		}
	}()
}

// Initialize 加载配置、打开数据库、注册 RPC 与健康检查，并启动后台循环。
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("加载事件注册表失败: %w", err)
	}
	dbStore, err := store.Open(cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{Store: dbStore}
	ready := false
	defer func() {
		if !ready {
			_ = runtime.Close()
		}
	}()
	if err := dbStore.ApplySchema(schema.AllSQL()); err != nil {
		return nil, fmt.Errorf("初始化数据库结构失败: %w", err)
	}
	if err := dbStore.CheckSchema(ctx); err != nil {
		return nil, fmt.Errorf("%w（库文件 %s）", err, cfg.Database.Path)
	}
	// 服务自带的系统规则：首次启动时创建，默认启用；用户停用过的不会被改回来。
	if err := dbStore.EnsureSystemRule(ctx, rpc.SystemRule(time.Now())); err != nil {
		return nil, fmt.Errorf("创建系统规则失败: %w", err)
	}

	// 系统动作经网关调用 Trade：网关打不开（没有安装身份、目录读不到）就不能启动，不静默退化成只记录。
	runtime.Gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "事件中心网关目录刷新失败: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("打开网关客户端失败: %w", err)
	}
	actions := action.NewRegistry()
	actions.Register(store.SystemActionSellDelistedHoldings, &delist.Handler{Store: dbStore, Trade: tradegateway.New(runtime.Gateway)})
	sender := delivery.NewSender(delivery.Config{ConnectTimeout: cfg.Delivery.ConnectTimeout, RequestTimeout: cfg.Delivery.RequestTimeout})
	intake := &dispatch.Intake{Store: dbStore, Registry: registry}
	worker := &dispatch.Worker{
		Store: dbStore, Sender: sender, Actions: actions, Batch: cfg.Delivery.WorkerBatch, PollInterval: cfg.Delivery.PollInterval,
		RetryWindow: cfg.Delivery.RetryWindow, RetentionDays: cfg.Retention.ExecutionDays,
	}

	var eventBusConnected atomic.Bool
	supervisor := &eventconsumer.Supervisor{
		Config:            func() (jetstream.Config, error) { return eventBusConfig(cfg) },
		Registry:          registry,
		Handler:           eventconsumer.Handler{Processor: intake},
		SetConnected:      eventBusConnected.Store,
		ReconnectInterval: reconnectInterval,
		FetchMaxWait:      time.Second,
		RetryInterval:     5 * time.Second,
	}

	runCtx, cancel := context.WithCancel(ctx)
	runtime.cancel = cancel
	runtime.start(runCtx, "执行记录工作线程", worker.Run)
	runtime.start(runCtx, "关注项上报", (&dispatch.AttentionWorker{Store: dbStore, Interval: attentionInterval}).Run)
	runtime.start(runCtx, "事件消费", supervisor.Run)

	service := &rpc.Service{Store: dbStore, Sender: sender, Actions: actions}
	pb.RegisterCallbackDispatcherServiceService(s.Service("trpc.moox.callbackdispatcher.CallbackDispatcherService"), service)
	if err := registerMetricsReporter(s); err != nil {
		return nil, err
	}
	if err := registerHealth(s, dbStore, actions, &eventBusConnected); err != nil {
		return nil, err
	}

	ready = true
	s.RegisterOnShutdown(func() { _ = runtime.Close() })
	log.InfoContextf(ctx, "moox-callback-dispatcher 初始化完成")
	return s, nil
}

// eventBusConfig 每次连接时重新读取凭据文件。
func eventBusConfig(cfg *config.Config) (jetstream.Config, error) {
	clientConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, "moox-callback-dispatcher")
	if cfg.EventBus.CredentialFile != "" {
		if err := clientConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
			return clientConfig, err
		}
	}
	return clientConfig, nil
}

func registerMetricsReporter(s *server.Server) error {
	handler, err := report.NewHandler(report.DefaultConfig(moduleName, "callback-dispatcher"))
	if err != nil {
		return fmt.Errorf("创建指标上报失败: %w", err)
	}
	service := s.Service("trpc.moox.callbackdispatcher.metrics.timer")
	if service == nil {
		return fmt.Errorf("没有配置指标上报的定时服务")
	}
	timer.RegisterHandlerService(service, handler.Handle)
	return nil
}

func registerHealth(s *server.Server, dbStore *store.Store, actions *action.Registry, eventBusConnected *atomic.Bool) error {
	state := health.New("callback-dispatcher", "callback-dispatcher", "", "")
	state.SnapshotFunc = func(ctx context.Context) healthz.Response {
		databaseReady := dbStore.Ping(ctx) == nil
		state.SetReady(databaseReady)
		response := healthz.Base("callback-dispatcher", "callback-dispatcher", "", "", state.StartedAt, databaseReady)
		response.Details = map[string]any{
			"database":       databaseReady,
			"eventbus":       eventBusConnected.Load(),
			"system_actions": actions.Actions(),
		}
		return response
	}
	if err := health.Register(s.Service("trpc.moox.callbackdispatcher.Health"), state); err != nil {
		return fmt.Errorf("注册健康检查失败: %w", err)
	}
	return nil
}
