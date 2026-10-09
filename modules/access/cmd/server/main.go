// moox-access 是外部接入：接收 SCF、因子引擎和 moox-skill 等外部调用方的签名请求，按组件目录中的白名单
// 放行后，以 access 身份经本机主机网关转发。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mooyang-code/moox/modules/access/internal/accessproxy"
	"github.com/mooyang-code/moox/modules/access/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

const (
	healthServiceName   = "trpc.moox.access.Health"
	readyCheckInterval  = 5 * time.Second
	readyCheckTimeout   = 3 * time.Second
	defaultConfigPath   = "./config/app.yaml"
	healthModuleName    = "access"
	healthInstanceName  = "access"
	upstreamCallerLabel = "access"
)

func main() {
	trpclog.InstallServiceName(healthModuleName)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load(defaultConfigPath)
	if err != nil {
		return err
	}
	catalog := servicecatalog.Default()
	registry, principals, err := accessproxy.LoadPrincipalKeys(cfg.PrincipalsFile, catalog)
	if err != nil {
		return err
	}
	log.Printf("外部接入登记的外部调用方: %v", principals)
	nonces, err := accessproxy.OpenSQLiteNonces(cfg.NoncePath)
	if err != nil {
		return err
	}
	defer nonces.Close()
	gateway, err := gatewayclient.New(gatewayclient.Options{Config: cfg.GatewayClient, Catalog: catalog})
	if err != nil {
		return fmt.Errorf("创建 %s 的 gatewayclient: %w", upstreamCallerLabel, err)
	}
	defer gateway.Close()
	metrics, err := accessproxy.NewPrometheusMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return err
	}
	proxy, err := accessproxy.New(accessproxy.Options{
		Registry: registry, Catalog: catalog, Upstream: gateway, Nonces: nonces, Metrics: metrics,
		MaxBodyBytes: cfg.MaxBodyBytes, Timeout: cfg.Timeout,
	})
	if err != nil {
		return err
	}

	s := trpc.NewServer(server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	if err := accessproxy.RegisterAccessService(s.Service(accessproxy.AccessServiceName), proxy); err != nil {
		return fmt.Errorf("注册外部接入服务: %w", err)
	}
	stopHealth, err := registerHealth(s, gateway)
	if err != nil {
		return err
	}
	defer stopHealth()
	return s.Serve()
}

// registerHealth 注册健康检查：拿到本机的服务目录后才就绪，否则无法确定自己的实例 ID，也无法转发。
func registerHealth(s *server.Server, gateway *gatewayclient.Client) (func(), error) {
	service := s.Service(healthServiceName)
	if service == nil {
		return func() {}, errors.New("外部接入的健康检查服务未配置")
	}
	state := healthz.NewState(healthModuleName, healthInstanceName, Version, GitCommit)
	state.SetReady(false)
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return func() {}, err
	}
	if err := healthz.RegisterNoProtocolServiceMux(service, handler); err != nil {
		return func() {}, fmt.Errorf("注册外部接入健康检查: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(readyCheckInterval)
		defer ticker.Stop()
		for {
			checkCtx, checkCancel := context.WithTimeout(ctx, readyCheckTimeout)
			view, err := gateway.Directory(checkCtx)
			checkCancel()
			ready := err == nil && view.LocalHostID != ""
			if ready != state.Ready() {
				log.Printf("外部接入就绪状态变为 %t（本机主机 %q，错误 %v）", ready, view.LocalHostID, err)
			}
			state.SetReady(ready)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel, nil
}
