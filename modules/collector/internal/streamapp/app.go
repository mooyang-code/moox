package streamapp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	"github.com/mooyang-code/moox/modules/collector/internal/localexec"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

// App 是装配好的 collector-stream。
type App struct {
	Config    *Config
	Gateway   *gatewayclient.Client
	Runtime   *stream.Runtime
	Agent     *stream.Agent
	Executor  *localexec.Executor
	Scheduler *localexec.Scheduler
	dns       *dnscache.Cache
}

// New 打开网关客户端并装配运行时、同步代理与本地执行端（不启动）。
func New(cfg *Config, version string, registerer prometheus.Registerer) (*App, error) {
	// 数据源适配器与 Storage 绑定都从这个文件读：WS 地址与限额、REST 端点、AppId。
	if err := os.Setenv("MOOX_STORAGE_MARKET_CONFIG", cfg.MarketConfig); err != nil {
		return nil, err
	}
	gateway, err := cfg.GatewayClient.OpenInternal(cfg.SourcePath(), cfg.DataDir, func(err error) { log.Warnf("collector-stream directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("open gateway client: %w", err)
	}
	streamConfig, err := binance.ResolveStreamConfig()
	if err != nil {
		_ = gateway.Close()
		return nil, fmt.Errorf("binance stream config: %w", err)
	}
	app := &App{Config: cfg, Gateway: gateway}
	app.dns = dnscache.New(dnscache.Config{Domains: endpointHosts(append(append([]string(nil), streamConfig.SpotEndpoints...), streamConfig.SwapEndpoints...))})
	storageTimeout := time.Duration(cfg.StorageTimeoutMS) * time.Millisecond
	newStorage := func(marketType string) (marketfetch.Storage, error) {
		return marketfetch.NewMarketStorageForMarket(gateway, marketType)
	}
	app.Executor = localexec.New(cfg.SpaceID, cfg.LocalExecutor.Concurrency, func() *marketfetch.Handler {
		handler := marketwiring.NewHandler()
		handler.SpaceID = cfg.SpaceID
		handler.NewStorage = newStorage
		return handler
	})
	app.Scheduler = localexec.NewScheduler(app.Executor)
	providers, err := stream.ProvidersFrom(catalog.Default(), marketdata.Deps{})
	if err != nil {
		_ = gateway.Close()
		return nil, err
	}
	var agent *stream.Agent
	app.Runtime = stream.NewRuntime(stream.RuntimeConfig{
		SpaceID:   cfg.SpaceID,
		Providers: providers,
		NewCommitter: func(market marketfetch.PlanMarket) (stream.PeriodCommitter, error) {
			storage, err := newStorage(market.MarketType)
			if err != nil {
				return nil, err
			}
			return &timedCommitter{committer: marketfetch.NewKlineCommitter(storage, market), timeout: storageTimeout}, nil
		},
		Manager: stream.ManagerOptions{Resolve: app.resolve},
		Metrics: stream.NewMetrics(registerer),
		Report:  func(stat stream.PeriodStat) { agent.Collect(stat) },
		Suspect: func(suspect stream.Suspect) { agent.Flag(suspect) },
	})
	agent = stream.NewAgent(&stream.PlanClient{Gateway: gateway, SpaceID: cfg.SpaceID, InstanceID: cfg.InstanceID, Version: version}, app.Runtime, app.Scheduler)
	app.Agent = agent
	return app, nil
}

// Start 启动运行时、本地定时拉取与计划同步。
func (a *App) Start(ctx context.Context) {
	if err := a.dns.Refresh(ctx); err != nil {
		log.Warnf("collector-stream initial DNS refresh failed: %v", err)
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if a.dns.Due(now) {
					if err := a.dns.Refresh(ctx); err != nil {
						log.Warnf("collector-stream DNS refresh failed: %v", err)
					}
				}
			}
		}
	}()
	a.Runtime.Start(ctx)
	go a.Scheduler.Run(ctx)
	go a.Agent.Run(ctx)
}

// Ready 表示已应用 Collector 要求的计划版本，且全部市场的连接都已订阅完成。
func (a *App) Ready() bool { return a.Agent.Synced() && a.Runtime.Ready() }

// Close 关闭网关客户端。
func (a *App) Close() error { return a.Gateway.Close() }

func (a *App) resolve(host string) []string {
	return a.dns.Snapshot()[host].IPs
}

func endpointHosts(endpoints []string) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" || seen[parsed.Hostname()] {
			continue
		}
		seen[parsed.Hostname()] = true
		hosts = append(hosts, strings.ToLower(parsed.Hostname()))
	}
	return hosts
}

// timedCommitter 给单次周期提交加上 Storage 写入超时（提交队列另按截止时间约束整体重试）。
type timedCommitter struct {
	committer *marketfetch.KlineCommitter
	timeout   time.Duration
}

func (c *timedCommitter) CommitPeriod(ctx context.Context, batch marketfetch.KlineBatch) (marketfetch.KlineCommitResult, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	return c.committer.CommitPeriod(ctx, batch)
}
