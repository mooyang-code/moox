// Package bootstrap wires the independent moox-cloudnode process.
package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/health"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Runtime owns CloudNode process resources and their shutdown order.
type Runtime struct {
	StartedAt       time.Time
	Store           *store.Store
	JetStream       *jobqueue.Runtime
	DebugServer     *http.Server
	NodeBatchCancel context.CancelFunc
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if r.NodeBatchCancel != nil {
		r.NodeBatchCancel()
	}
	var firstErr error
	if r.JetStream != nil {
		if err := r.JetStream.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.DebugServer != nil {
		if err := r.DebugServer.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.Store != nil {
		if err := r.Store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Initialize loads config, initializes persistence, and registers tRPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	log.InfoContextf(ctx, "开始初始化 moox-cloudnode...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 cloudnode 配置失败: %v", err)
		return nil, err
	}
	runtime := &Runtime{StartedAt: time.Now()}

	dbm, err := store.Open(&cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 cloudnode 数据库失败: %v", err)
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
		log.ErrorContextf(ctx, "初始化 cloudnode schema 失败: %v", err)
		return nil, err
	}
	historyStore := jobhistory.NewStore(jobhistory.StoreOptions{
		Dir:           cfg.JobItem.HistoryDir,
		RetentionDays: cfg.JobItem.HistoryRetentionDays,
	})
	cloudnoderpc.SetDefaultJobHistoryMaintainer(historyStore)
	registerJobHistorySchedule(s)
	moduleMetrics, err := registerMetricsReporter(s)
	if err != nil {
		return nil, err
	}

	opts := []cloudnoderpc.Option{cloudnoderpc.WithModuleMetrics(moduleMetrics)}
	if cfg.Queue.Backend == "jetstream" && cfg.JetStream.Enabled {
		rt, err := jobqueue.Connect(ctx, cfg.JetStream)
		if err != nil {
			log.ErrorContextf(ctx, "初始化 cloudnode JetStream 失败: %v", err)
			return nil, err
		}
		if err := rt.EnsureStreams(cfg.JetStream, cfg.JobItem); err != nil {
			log.ErrorContextf(ctx, "初始化 cloudnode JetStream stream 失败: %v", err)
			_ = rt.Close()
			return nil, err
		}
		runtime.JetStream = rt
		kv, err := rt.BindKV(ctx, cfg.JobItem.ActiveKVBucket)
		if err != nil {
			log.ErrorContextf(ctx, "打开 cloudnode JobItem active KV 失败: %v", err)
			return nil, err
		}
		stateStore := jobstate.NewKVStore(kv, jobstate.Options{})
		execQueue := jobqueue.NewJetStreamQueue(rt, jobqueue.QueueConfig{
			AckWait:       jobqueue.DefaultAckWait,
			MaxDeliver:    cfg.JetStream.MaxDeliver,
			MaxAckPending: cfg.JetStream.MaxAckPending,
		})
		opts = append(opts,
			cloudnoderpc.WithExecutionQueue(execQueue),
			cloudnoderpc.WithJobStateStore(stateStore),
			cloudnoderpc.WithJobHistoryStore(historyStore),
		)
		log.InfoContextf(ctx, "cloudnode JetStream 已启用: event=%s active_kv=%s eventbus_urls=%s",
			events.CloudJobExecutionRequested.Name(), cfg.JobItem.ActiveKVBucket, strings.Join(cfg.JetStream.URLs, ","))
	}

	credentialResolver, err := cloudcredential.NewFromEnv()
	if err != nil {
		return nil, err
	}
	opts = append(opts, cloudnoderpc.WithCredentialResolver(credentialResolver))
	svc := cloudnoderpc.New(dbm, opts...)
	nodeBatchCtx, nodeBatchCancel := context.WithCancel(ctx)
	runtime.NodeBatchCancel = nodeBatchCancel
	if err := startNodeBatchRunner(nodeBatchCtx, svc, cfg); err != nil {
		return nil, err
	}
	cloudnodepb.RegisterCloudNodeMgrService(s.Service("trpc.moox.cloudnode.CloudNodeMgr"), svc)
	if err := registerHealth(s, cfg, dbm); err != nil {
		return nil, err
	}

	keepResources = true
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			closeCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
			defer cancel()
			_ = runtime.Close(closeCtx)
		}()
	}
	log.InfoContextf(ctx, "moox-cloudnode 初始化完成")
	return s, nil
}

func startNodeBatchRunner(ctx context.Context, svc *cloudnoderpc.Service, cfg *config.Config) error {
	if svc == nil {
		return fmt.Errorf("cloudnode node batch service is required")
	}
	if cfg == nil {
		return fmt.Errorf("cloudnode config is required")
	}
	return svc.StartNodeBatchRunner(ctx, cfg.NodeBatch.BatchSize, cfg.NodeBatch.PollInterval)
}

func registerMetricsReporter(s *server.Server) (*report.ModuleMetrics, error) {
	if s == nil {
		return nil, fmt.Errorf("cloudnode metrics reporter requires a tRPC server")
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "cloudnode", report.HealthCheckIDsForModule("cloudnode"))
	if err != nil {
		return nil, err
	}
	h, err := report.NewHandler(report.DefaultConfig("cloudnode", "moox_cloudnode"))
	if err != nil {
		return nil, err
	}
	service := s.Service("trpc.moox.cloudnode.metrics.timer")
	if service == nil {
		return nil, fmt.Errorf("cloudnode metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return moduleMetrics, nil
}

func registerHealth(s *server.Server, cfg *config.Config, dbm *store.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("cloudnode", "cloudnode", "", "")
	state.SnapshotFunc = cloudnodeHealthSnapshot(cfg, dbm, state)
	if s == nil {
		return fmt.Errorf("cloudnode health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.cloudnode.Health"), state); err != nil {
		return fmt.Errorf("cloudnode health server failed to start: %w", err)
	}
	return nil
}

func cloudnodeHealthSnapshot(cfg *config.Config, dbm *store.Store, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		databaseReady := dbm != nil && dbm.Ping(ctx) == nil
		state.SetReady(databaseReady)
		rsp := healthz.Base("cloudnode", "cloudnode", "", "", state.StartedAt, databaseReady)
		rsp.Details = map[string]any{
			"database":          databaseReady,
			"queue_backend":     cfg.Queue.Backend,
			"jetstream_enabled": cfg.JetStream.Enabled,
		}
		return rsp
	}
}

func registerJobHistorySchedule(s *server.Server) {
	timer.RegisterScheduler("cloudnodeJobHistorySchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.cloudnode.jobhistory.timer")
	if service == nil {
		log.Warn("cloudnode job history timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		return cloudnoderpc.HandleJobHistorySchedule(ctx, "")
	})
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
		log.InfoContextf(ctx, "cloudnode pprof listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.ErrorContextf(ctx, "cloudnode pprof server failed: %v", err)
		}
	}()
	return server
}
