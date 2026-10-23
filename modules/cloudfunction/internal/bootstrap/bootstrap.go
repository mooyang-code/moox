// Package bootstrap wires the independent moox-cloudfunction process.
package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/clsprovision"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/config"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/health"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/publishlease"
	cloudfunctionrpc "github.com/mooyang-code/moox/modules/cloudfunction/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/cloudfunction/schema"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Runtime owns FunctionInstance process resources and their shutdown order.
type Runtime struct {
	StartedAt         time.Time
	Store             *store.Store
	DebugServer       *http.Server
	FunctionJobCancel context.CancelFunc
	FunctionJobWait   func()
	Gateway           *gatewayclient.Client
	closeOnce         sync.Once
	closeErr          error
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	r.closeOnce.Do(func() {
		if r.FunctionJobCancel != nil {
			r.FunctionJobCancel()
		}
		if r.FunctionJobWait != nil {
			r.FunctionJobWait()
		}
		if r.DebugServer != nil {
			r.closeErr = r.DebugServer.Shutdown(ctx)
		}
		if r.Gateway != nil {
			if err := r.Gateway.Close(); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
		if r.Store != nil {
			if err := r.Store.Close(); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
	})
	return r.closeErr
}

// Initialize loads config, initializes persistence, and registers tRPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	log.InfoContextf(ctx, "开始初始化 moox-cloudfunction...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 cloudfunction 配置失败: %v", err)
		return nil, err
	}
	if err := tencentscf.ConfigureRateLimits(cfg.TencentSCF.APIRateLimits); err != nil {
		return nil, err
	}
	runtime := &Runtime{StartedAt: time.Now()}

	dbm, err := store.Open(&cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 cloudfunction 数据库失败: %v", err)
		return nil, err
	}
	runtime.Store = dbm
	runtime.DebugServer = startDebugServer(ctx, cfg.Debug.PprofAddr)
	keepResources := false
	defer func() {
		if !keepResources {
			closeCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
			defer cancel()
			_ = runtime.Close(closeCtx)
		}
	}()
	if err := dbm.ApplySchema(schema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 cloudfunction schema 失败: %v", err)
		return nil, err
	}
	runtime.Gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "cloudfunction directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("initialize cloudfunction gateway: %w", err)
	}
	moduleMetrics, err := registerMetricsReporter(s)
	if err != nil {
		return nil, err
	}

	opts := []cloudfunctionrpc.Option{
		cloudfunctionrpc.WithModuleMetrics(moduleMetrics),
		cloudfunctionrpc.WithCollectorPublishLeaseValidator(publishlease.New(runtime.Gateway)),
	}
	credentialResolver, err := cloudcredential.New(runtime.Gateway)
	if err != nil {
		return nil, err
	}
	opts = append(opts, cloudfunctionrpc.WithCredentialResolver(credentialResolver))
	// 日志服务（CLS）自动开通与下发：创建或更新采集函数时补齐上报环境，失败不阻断。
	opts = append(opts, cloudfunctionrpc.WithCLSEnvironment(clsprovision.New(cfg.CLS, nil)))
	svc := cloudfunctionrpc.New(dbm, opts...)
	functionJobCtx, functionJobCancel := context.WithCancel(ctx)
	runtime.FunctionJobCancel = functionJobCancel
	if err := startFunctionJobRunner(functionJobCtx, svc, cfg); err != nil {
		return nil, err
	}
	runtime.FunctionJobWait = svc.WaitFunctionJobRunner
	cloudfunctionpb.RegisterCloudFunctionMgrService(s.Service("trpc.moox.cloudfunction.CloudFunctionMgr"), svc)
	if err := registerHealth(s, cfg, dbm); err != nil {
		return nil, err
	}

	keepResources = true
	s.RegisterOnShutdown(func() {
		closeCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 20*time.Second)
		defer cancel()
		_ = runtime.Close(closeCtx)
	})
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			closeCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
			defer cancel()
			_ = runtime.Close(closeCtx)
		}()
	}
	log.InfoContextf(ctx, "moox-cloudfunction 初始化完成")
	return s, nil
}

func startFunctionJobRunner(ctx context.Context, svc *cloudfunctionrpc.Service, cfg *config.Config) error {
	if svc == nil {
		return fmt.Errorf("cloudfunction instance batch service is required")
	}
	if cfg == nil {
		return fmt.Errorf("cloudfunction config is required")
	}
	return svc.StartFunctionJobRunner(ctx, cfg.FunctionJob.BatchSize, cfg.FunctionJob.PollInterval)
}

func registerMetricsReporter(s *server.Server) (*report.ModuleMetrics, error) {
	if s == nil {
		return nil, fmt.Errorf("cloudfunction metrics reporter requires a tRPC server")
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "cloudfunction", report.HealthCheckIDsForModule("cloudfunction"))
	if err != nil {
		return nil, err
	}
	h, err := report.NewHandler(report.DefaultConfig("cloudfunction", "cloudfunction"))
	if err != nil {
		return nil, err
	}
	service := s.Service("trpc.moox.cloudfunction.metrics.timer")
	if service == nil {
		return nil, fmt.Errorf("cloudfunction metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return moduleMetrics, nil
}

func registerHealth(s *server.Server, cfg *config.Config, dbm *store.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("cloudfunction", "cloudfunction", "", "")
	state.SnapshotFunc = cloudfunctionHealthSnapshot(cfg, dbm, state)
	if s == nil {
		return fmt.Errorf("cloudfunction health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.cloudfunction.Health"), state); err != nil {
		return fmt.Errorf("cloudfunction health server failed to start: %w", err)
	}
	return nil
}

func cloudfunctionHealthSnapshot(cfg *config.Config, dbm *store.Store, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		databaseReady := dbm != nil && dbm.Ping(ctx) == nil
		state.SetReady(databaseReady)
		rsp := healthz.Base("cloudfunction", "cloudfunction", "", "", state.StartedAt, databaseReady)
		rsp.Details = map[string]any{
			"database": databaseReady,
		}
		return rsp
	}
}

func startDebugServer(ctx context.Context, addr string) *http.Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	mux := healthz.NewMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandlePrefix("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))

	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.InfoContextf(ctx, "cloudfunction pprof listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.ErrorContextf(ctx, "cloudfunction pprof server failed: %v", err)
		}
	}()
	return server
}
