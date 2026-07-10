// Package bootstrap wires the independent moox-cloudnode process.
package bootstrap

import (
	"context"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/metricspublish"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var cloudnodeStartedAt = time.Now()

// Initialize loads config, initializes persistence, and registers tRPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-cloudnode...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 cloudnode 配置失败: %v", err)
		return nil, err
	}
	config.SetGlobalConfig(cfg)
	startDebugServer(ctx, cfg.Debug.PprofAddr)

	dbm := storage.NewManager()
	if err := dbm.Initialize(&cfg.Database); err != nil {
		log.ErrorContextf(ctx, "初始化 cloudnode 数据库失败: %v", err)
		return nil, err
	}
	startHealthServer(ctx, cfg)

	historyStore := jobhistory.NewStore(jobhistory.StoreOptions{
		Dir:           cfg.JobItem.HistoryDir,
		RetentionDays: cfg.JobItem.HistoryRetentionDays,
	})
	cloudnoderpc.SetDefaultJobHistoryMaintainer(historyStore)
	registerJobHistorySchedule(s)
	registerMetricsReporter(s)

	opts := []cloudnoderpc.Option{}
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
		kv, err := rt.KeyValue(cfg.JobItem.ActiveKVBucket)
		if err != nil {
			log.ErrorContextf(ctx, "打开 cloudnode JobItem active KV 失败: %v", err)
			_ = rt.Close()
			return nil, err
		}
		stateStore := jobstate.NewKVStore(kv, jobstate.Options{
			RecoverAfterMillis: cfg.JobItem.RecoverAfterMillis,
			DefaultMaxAttempts: cfg.JobItem.DefaultMaxAttempts,
		})
		execQueue := jobqueue.NewJetStreamQueue(rt, jobqueue.QueueConfig{
			Naming:          jobqueue.NamingConfig{SubjectPrefix: cfg.JetStream.SubjectPrefix},
			ExecStream:      cfg.JetStream.ExecStream,
			AckWait:         time.Duration(cfg.JetStream.AckWaitMillis) * time.Millisecond,
			MaxDeliver:      cfg.JetStream.MaxDeliver,
			FetchMaxWait:    time.Duration(cfg.JetStream.FetchMaxWaitMs) * time.Millisecond,
			DefaultMaxBatch: cfg.JobItem.MaxLimit,
		})
		catalog := repository.NewCatalogRepository(dbm.DB())
		heartbeatSink := projection.NewHeartbeatBuffer(catalog, projection.HeartbeatBufferOptions{
			MaxKeys:       2048,
			FlushInterval: time.Second,
		})
		opts = append(opts,
			cloudnoderpc.WithExecutionQueue(execQueue),
			cloudnoderpc.WithJobStateStore(stateStore),
			cloudnoderpc.WithJobHistoryStore(historyStore),
			cloudnoderpc.WithHeartbeatSink(heartbeatSink),
		)
		log.InfoContextf(ctx, "cloudnode JetStream 已启用: exec_stream=%s active_kv=%s nats_url=%s",
			cfg.JetStream.ExecStream, cfg.JobItem.ActiveKVBucket, cfg.JetStream.NATSURL)
	}

	svc := cloudnoderpc.New(dbm, opts...)
	cloudnodepb.RegisterCloudNodeMgrService(s.Service("trpc.moox.cloudnode.CloudNodeMgr"), svc)

	log.InfoContextf(ctx, "moox-cloudnode 初始化完成")
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil { return }
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("moox-cloudnode"))
	if err != nil { log.Warnf("cloudnode metrics reporter disabled: %v", err); return }
	service := s.Service("trpc.moox.cloudnode.metrics.timer")
	if service == nil { log.Warn("cloudnode metrics timer service is not configured, skip register"); return }
	timer.RegisterHandlerService(service, h.Handle)
}

func startHealthServer(ctx context.Context, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if _, err := healthz.Start(ctx, cfg.Health.Addr, cloudnodeHealthSnapshot(cfg)); err != nil {
		log.ErrorContextf(ctx, "cloudnode health server failed to start: %v", err)
	}
}

func cloudnodeHealthSnapshot(cfg *config.Config) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := healthz.Base("cloudnode", "cloudnode", "", "", cloudnodeStartedAt, true)
		rsp.Details = map[string]any{
			"database":          "ok",
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
	timer.RegisterHandlerService(service, cloudnoderpc.HandleJobHistorySchedule)
}

func startDebugServer(ctx context.Context, addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
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
}
