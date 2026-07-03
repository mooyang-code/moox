// Package bootstrap wires the independent moox-cloudnode process.
package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize loads config, initializes persistence, and registers tRPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-cloudnode...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 cloudnode 配置失败: %v", err)
		return nil, err
	}
	config.SetGlobalConfig(cfg)

	dbm := storage.NewManager()
	if err := dbm.Initialize(&cfg.Database); err != nil {
		log.ErrorContextf(ctx, "初始化 cloudnode 数据库失败: %v", err)
		return nil, err
	}

	svc := cloudnoderpc.New(dbm)
	cloudnodepb.RegisterCloudNodeMgrService(s.Service("trpc.moox.cloudnode.CloudNodeMgr"), svc)

	log.InfoContextf(ctx, "moox-cloudnode 初始化完成")
	return s, nil
}
