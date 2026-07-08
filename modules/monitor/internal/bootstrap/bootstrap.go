// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var (
	monitorStartedAt = time.Now()
	defaultManager   *monstorage.Manager
)

func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-monitor...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 monitor 配置失败: %v", err)
		return nil, err
	}
	mgr, err := monstorage.OpenFromConfig(cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 monitor 数据库失败: %v", err)
		return nil, err
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "初始化 monitor schema 失败: %v", err)
		return nil, err
	}
	defaultManager = mgr
	startHealthServer(ctx, cfg)
	registerMonitorService(s, cfg, mgr)

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
}

func Manager() *monstorage.Manager {
	return defaultManager
}

func startHealthServer(ctx context.Context, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if _, err := healthz.Start(ctx, cfg.Health.Addr, monitorHealthSnapshot(cfg)); err != nil {
		log.ErrorContextf(ctx, "monitor health server failed to start: %v", err)
	}
}

func registerMonitorService(s *server.Server, cfg *config.Config, mgr *monstorage.Manager) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(mgr.DB(), monitorrpc.Options{
		InstanceID: cfg.Instance.InstanceID,
	}))
}

func monitorHealthSnapshot(cfg *config.Config) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := healthz.Base("monitor", cfg.Instance.InstanceID, "", "", monitorStartedAt, true)
		rsp.Details = map[string]any{
			"database":          "ok",
			"scheduler":         "configured",
			"peer_enabled":      cfg.Peer.Enabled,
			"sysdeploy_enabled": cfg.SysDeploy.Enabled,
		}
		return rsp
	}
}
