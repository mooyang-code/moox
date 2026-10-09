// moox-egress-proxy 是出口代理：挂在主机网关后面，替 MooX 内部组件（目前只有 Collector）访问国内连不上的外部
// HTTPS 接口，并解析这些域名的可用地址。部署在 compute-1（香港）。
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/config"
	"github.com/mooyang-code/moox/modules/egressproxy/internal/proxy"
	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
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
	healthServiceName = "trpc.moox.egress.Health"
	defaultConfigPath = "./config/app.yaml"
	componentName     = "egress-proxy"
)

func main() {
	trpclog.InstallServiceName(componentName)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load(defaultConfigPath)
	if err != nil {
		return err
	}
	var dns *resolver.Resolver
	if len(cfg.DNS.Domains) > 0 {
		dnsMetrics, err := resolver.NewMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			return fmt.Errorf("注册 DNS 解析指标: %w", err)
		}
		dns = resolver.New(resolver.Config{
			Domains: cfg.DNS.Domains, LookupTimeout: cfg.DNS.LookupTimeout, ProbeTimeout: cfg.DNS.ProbeTimeout,
			ProbePort: cfg.DNS.ProbePort, CacheTTL: cfg.DNS.CacheTTL, MaxIPsPerDomain: cfg.DNS.MaxIPsPerDomain, Metrics: dnsMetrics,
		})
	}
	metrics, err := proxy.NewMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("注册出口代理指标: %w", err)
	}
	egress, err := proxy.New(proxy.Config{
		Domains: cfg.HTTP.Domains, Headers: cfg.HTTP.AllowedHeaders,
		MaxResponseBytes: cfg.HTTP.MaxResponseBytes, DefaultTimeout: cfg.HTTP.DefaultTimeout,
	}, dns, metrics)
	if err != nil {
		return err
	}

	s := trpc.NewServer()
	service := s.Service(proxy.ServiceName)
	if service == nil {
		return fmt.Errorf("trpc_go.yaml 没有配置服务 %s", proxy.ServiceName)
	}
	egresspb.RegisterProxyService(service, egress)
	if err := registerHealth(s); err != nil {
		return err
	}
	log.Printf("出口代理已启动：放行域名 %v，DNS 解析域名 %d 个", cfg.HTTP.Domains, len(cfg.DNS.Domains))
	return s.Serve()
}

// registerHealth 注册健康检查；出口代理没有外部依赖，配置加载成功即就绪。
func registerHealth(s *server.Server) error {
	service := s.Service(healthServiceName)
	if service == nil {
		return errors.New("出口代理的健康检查服务未配置")
	}
	state := healthz.NewState(componentName, componentName, Version, GitCommit)
	state.SetReady(true)
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return err
	}
	if err := healthz.RegisterNoProtocolServiceMux(service, handler); err != nil {
		return fmt.Errorf("注册出口代理健康检查: %w", err)
	}
	return nil
}
