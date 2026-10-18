// Package bootstrap wires the moox-archive-mgr process.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	archivehealth "github.com/mooyang-code/moox/modules/archive/internal/health"
	"github.com/mooyang-code/moox/modules/archive/internal/service"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	healthService   = "trpc.moox.archive.Health"
	mgrService      = "trpc.moox.archive.ArchiveMgr"
	workerService   = "trpc.moox.archive.ArchiveWorker"
	triggerService  = "trpc.moox.archive.mgr.trigger.timer"
	maintainService = "trpc.moox.archive.mgr.maintain.timer"
	metricsService  = "trpc.moox.archive.mgr.metrics.timer"

	triggerTimeout  = 900 * time.Millisecond
	maintainTimeout = 50 * time.Second
	metricsTimeout  = 10 * time.Second
)

// Runtime 持有 mgr 的长期资源。
type Runtime struct {
	gateway *gatewayclient.Client
	store   *store.Store
	health  *archivehealth.State
	once    sync.Once
	err     error
}

// Initialize 组装 mgr：打开 SQLite 与网关客户端，注册两个 RPC 服务、三个定时器（触发、维护、指标上报）和健康服务。
func Initialize(ctx context.Context, s *server.Server, cfg *config.Config) (_ *Runtime, err error) {
	if ctx == nil || s == nil || cfg == nil {
		return nil, errors.New("archive context, server and config are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	for _, name := range []string{healthService, mgrService, workerService, triggerService, maintainService, metricsService} {
		if s.Service(name) == nil {
			return nil, fmt.Errorf("archive service %q is required", name)
		}
	}
	runtime := &Runtime{health: archivehealth.New("archive", "archive-mgr", "", "")}
	defer func() {
		if err != nil {
			err = errors.Join(err, runtime.Close())
		}
	}()
	runtime.store, err = store.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open archive database: %w", err)
	}
	runtime.gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "archive-mgr gateway directory refresh: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("open archive Storage gateway client: %w", err)
	}
	svc, err := service.New(runtime.store, cfg, newGatewayDatasets(runtime.gateway))
	if err != nil {
		return nil, err
	}
	archivepb.RegisterArchiveMgrService(s.Service(mgrService), svc.Mgr())
	archivepb.RegisterArchiveWorkerService(s.Service(workerService), svc.Worker())
	if err := registerTimer(s, triggerService, "archive_trigger", triggerTimeout, svc.Trigger); err != nil {
		return nil, err
	}
	if err := registerTimer(s, maintainService, "archive_maintain", maintainTimeout, svc.Maintain); err != nil {
		return nil, err
	}
	if err := registerMetricsReporter(s, svc); err != nil {
		return nil, err
	}
	runtime.health.SnapshotFunc = runtime.healthSnapshot()
	if err := archivehealth.Register(s.Service(healthService), runtime.health); err != nil {
		return nil, fmt.Errorf("register archive health service: %w", err)
	}
	runtime.health.SetReady(true)
	return runtime, nil
}

func registerTimer(s *server.Server, service, name string, timeout time.Duration, run func(context.Context) error) error {
	job, err := timerjob.New(name, timeout, run)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(s.Service(service), job.Handle)
	return nil
}

// registerMetricsReporter 每 30 秒评估一次归档 worker 的在线状态并上报指标快照，Monitor 据此发出“归档 worker 离线”告警。
func registerMetricsReporter(s *server.Server, svc *service.Service) error {
	handler, err := report.NewHandler(report.DefaultConfig("archive", "archive-mgr"))
	if err != nil {
		return fmt.Errorf("create archive metrics reporter: %w", err)
	}
	presence, err := report.NewWorkerPresence(prometheus.DefaultRegisterer, "archive")
	if err != nil {
		return fmt.Errorf("create archive worker presence metric: %w", err)
	}
	return registerTimer(s, metricsService, "archive_metrics", metricsTimeout, func(ctx context.Context) error {
		if state, workerID, last, err := svc.Presence(ctx); err != nil {
			log.WarnContextf(ctx, "archive worker presence not evaluated: %v", err)
		} else {
			presence.Set(state, workerID, last)
		}
		return handler.Handle(ctx)
	})
}

// Close 释放资源，幂等。
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.health != nil {
			r.health.SetReady(false)
		}
		if r.gateway != nil {
			r.err = errors.Join(r.err, r.gateway.Close())
		}
		if r.store != nil {
			r.err = errors.Join(r.err, r.store.Close())
		}
	})
	return r.err
}

func (r *Runtime) healthSnapshot() healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		if ctx == nil {
			ctx = context.Background()
		}
		ready := r.store != nil && r.store.Ping(ctx) == nil
		status := "ok"
		var reasons []string
		if !ready {
			status = "error"
			reasons = append(reasons, "sqlite unavailable")
		}
		return healthz.Response{
			Module: "archive", Ready: ready, Status: status, Time: time.Now().UTC(),
			Details: map[string]any{"reasons": reasons},
		}
	}
}
